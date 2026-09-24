package similar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"evasimilar/internal/config"
	"evasimilar/internal/embed"
	"evasimilar/internal/eva"
	"evasimilar/internal/qdrant"
	"evasimilar/internal/textutil"
)

// TaskDoc is the normalized task material used for embeddings.
type TaskDoc struct {
	ID          string
	Title       string
	Description string
	Result      string
	Comments    []string
	Payload     map[string]any
	Vector      []float32
}

// Match is one similar task found in Qdrant.
type Match struct {
	ID            string
	Score         float32
	Title         string
	AlreadyLinked bool
}

type Service struct {
	cfg   config.Config
	eva   *eva.Client
	embed *embed.Client
	qd    *qdrant.Client
	log   *slog.Logger
}

func New(cfg config.Config, ec *eva.Client, emb *embed.Client, qc *qdrant.Client, log *slog.Logger) *Service {
	return &Service{cfg: cfg, eva: ec, embed: emb, qd: qc, log: log}
}

// EnsureCollection creates the Qdrant collection for tasks.
func (s *Service) EnsureCollection(ctx context.Context) error {
	return s.qd.EnsureCollection(ctx, s.cfg.QdrantCollection, s.embed.Dim(), s.cfg.QdrantDistance)
}

// IndexAll reads every task page from Eva, embeds and upserts into Qdrant.
func (s *Service) IndexAll(ctx context.Context) error {
	if s.cfg.IndexerRecreate {
		if err := s.qd.DropCollection(ctx, s.cfg.QdrantCollection); err != nil {
			s.log.Warn("drop collection", "err", err)
		}
		if err := s.EnsureCollection(ctx); err != nil {
			return err
		}
	}
	page := 0
	for {
		filter := map[string]any{}
		tasks, total, err := eva.ListTasks(ctx, s.eva, s.cfg.TaskListMethod, filter, page*s.cfg.IndexerPageSize, s.cfg.IndexerPageSize)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			break
		}
		s.log.Info("indexed page", "offset", page*s.cfg.IndexerPageSize, "page_size", len(tasks), "total", total)
		if err := s.IndexRaw(ctx, tasks); err != nil {
			return err
		}
		if len(tasks) < s.cfg.IndexerPageSize {
			break
		}
		page++
		if total > 0 && page*s.cfg.IndexerPageSize >= total {
			break
		}
	}
	return nil
}

// IndexRaw normalizes, embeds and upserts a batch of raw task maps.
func (s *Service) IndexRaw(ctx context.Context, tasks []map[string]any) error {
	docs := make([]*TaskDoc, 0, len(tasks))
	for _, raw := range tasks {
		doc, err := s.normalize(ctx, raw)
		if err != nil {
			s.log.Warn("normalize task", "err", err)
			continue
		}
		docs = append(docs, doc)
	}
	return s.embedAndStore(ctx, docs)
}

// IndexTask embeds a single task by its id and stores it in Qdrant.
func (s *Service) IndexTask(ctx context.Context, id any) (*TaskDoc, error) {
	raw, err := eva.GetTask(ctx, s.eva, s.cfg.TaskGetMethod, id)
	if err != nil {
		return nil, err
	}
	doc, err := s.normalize(ctx, raw)
	if err != nil {
		return nil, err
	}
	if err := s.embedAndStore(ctx, []*TaskDoc{doc}); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Service) embedAndStore(ctx context.Context, docs []*TaskDoc) error {
	for i := 0; i < len(docs); i += s.cfg.IndexerBatchSize {
		end := i + s.cfg.IndexerBatchSize
		if end > len(docs) {
			end = len(docs)
		}
		batch := docs[i:end]
		texts := make([]string, len(batch))
		for j, d := range batch {
			texts[j] = s.buildText(d)
		}
		vectors, err := s.embed.Embed(ctx, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(batch) {
			return fmt.Errorf("embedding count mismatch: got %d, want %d", len(vectors), len(batch))
		}
		points := make([]qdrant.Point, 0, len(batch))
		for j, d := range batch {
			d.Vector = vectors[j]
			d.Payload["eva_id"] = d.ID
			d.Payload["title"] = d.Title
			if _, ok := d.Payload["links"]; !ok {
				d.Payload["links"] = []string{}
			}
			pid := qdrant.PointID(d.ID)
			points = append(points, qdrant.Point{ID: pid, Vector: vectors[j], Payload: d.Payload})
		}
		if err := s.qd.Upsert(ctx, s.cfg.QdrantCollection, points); err != nil {
			return err
		}
		s.log.Debug("stored", "count", len(points))
	}
	return nil
}

// FindSimilar searches Qdrant for tasks similar to doc.
func (s *Service) FindSimilar(ctx context.Context, doc *TaskDoc) ([]Match, error) {
	pid := qdrant.PointID(doc.ID)
	hits, err := s.qd.Search(ctx, s.cfg.QdrantCollection, doc.Vector, s.cfg.ListenerMaxLinks, s.cfg.ListenerScoreThreshold, pid)
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, h := range hits {
		id := str(h.Payload["eva_id"])
		if id == "" || id == doc.ID {
			continue
		}
		already := false
		if links, ok := h.Payload["links"].([]any); ok {
			for _, l := range links {
				if fmt.Sprint(l) == doc.ID {
					already = true
					break
				}
			}
		}
		matches = append(matches, Match{
			ID:            id,
			Score:         h.Score,
			Title:         str(h.Payload["title"]),
			AlreadyLinked: already,
		})
	}
	return matches, nil
}

// FindAndLink is the listener entrypoint: index the new task, query the
// closest existing tasks and link them in Eva.
func (s *Service) FindAndLink(ctx context.Context, taskID any) (*TaskDoc, []Match, error) {
	doc, err := s.IndexTask(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	matches, err := s.FindSimilar(ctx, doc)
	if err != nil {
		return doc, nil, err
	}
	if len(matches) == 0 {
		return doc, nil, nil
	}

	newLinks := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.AlreadyLinked {
			s.log.Info("already linked", "from", doc.ID, "to", m.ID)
			continue
		}
		if !s.cfg.LinksDryRun {
			if err := eva.LinkTasks(ctx, s.eva, s.cfg.TaskLinkMethod, doc.ID, m.ID, "similar"); err != nil {
				s.log.Error("link failed", "from", doc.ID, "to", m.ID, "err", err)
				continue
			}
		}
		newLinks = append(newLinks, m.ID)
		s.log.Info("linked", "from", doc.ID, "to", m.ID, "score", m.Score, "dry_run", s.cfg.LinksDryRun)
	}

	if len(newLinks) > 0 {
		// Persist the new links on the task's own point.
		links, _ := doc.Payload["links"].([]any)
		for _, l := range newLinks {
			links = append(links, l)
		}
		doc.Payload["links"] = links
		points := []qdrant.Point{{ID: qdrant.PointID(doc.ID), Vector: doc.Vector, Payload: doc.Payload}}
		if err := s.qd.Upsert(ctx, s.cfg.QdrantCollection, points); err != nil {
			s.log.Warn("persist links", "err", err)
		}
	}
	return doc, matches, nil
}

// normalize converts a raw Eva task map into a TaskDoc using the configured
// field names (dotted paths allowed, e.g. "analysis.result").
func (s *Service) normalize(ctx context.Context, raw map[string]any) (*TaskDoc, error) {
	id := strp(lookup(raw, s.cfg.TaskIDField))
	if id == "" {
		return nil, fmt.Errorf("task has no id field %q", s.cfg.TaskIDField)
	}
	doc := &TaskDoc{
		ID:          id,
		Title:       strp(lookup(raw, s.cfg.TaskTitleField)),
		Description: strp(lookup(raw, s.cfg.TaskDescField)),
		Result:      strp(lookup(raw, s.cfg.TaskResultField)),
	}
	in, err := eva.ListComments(ctx, s.eva, s.cfg.TaskCommentsMethod, id, map[string]any{})
	if err != nil {
		s.log.Warn("fetch comments", "task", id, "err", err)
	} else {
		for _, c := range in {
			t := strp(lookup(c, s.cfg.TaskCommentField))
			if t != "" {
				doc.Comments = append(doc.Comments, t)
			}
		}
	}
	payload := map[string]any{}
	for _, f := range s.cfg.TaskPayloadFields {
		if v, ok := lookup(raw, f); ok {
			payload[f] = v
		}
	}
	doc.Payload = payload
	return doc, nil
}

func (s *Service) buildText(d *TaskDoc) string {
	return textutil.Join(s.cfg.EmbedMaxChars, d.Title, d.Description, d.Result, strings.Join(d.Comments, "\n"))
}

// lookup walks a dotted path inside a nested map.
func lookup(m map[string]any, path string) (any, bool) {
	cur := any(m)
	for _, part := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// strp converts a looked-up value plus its presence flag to a string.
func strp(v any, ok bool) string {
	if !ok {
		return ""
	}
	return str(v)
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprint(v)
	}
}
