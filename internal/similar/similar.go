package similar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"evasimilar/internal/config"
	"evasimilar/internal/embed"
	"evasimilar/internal/eva"
	"evasimilar/internal/qdrant"
	"evasimilar/internal/textutil"
)

// TaskDoc is the normalized task material used for embeddings.
//
// With EMBED_CHUNKING=semantic a task is split into semantic units:
// Chunks[i] is embedded into ChunkVectors[i] and stored as a separate Qdrant
// point (shared eva_id + chunk_index). With EMBED_CHUNKING=none there is a
// single aggregated chunk.
type TaskDoc struct {
	ID           string
	Code         string
	Title        string
	Description  string
	Result       string
	Comments     []string
	Payload      map[string]any
	Chunks       []string
	ChunkVectors [][]float32
	Vector       []float32 // first chunk vector (kept for compatibility)
}

// Match is one similar task found in Qdrant.
type Match struct {
	ID            string
	Code          string
	Score         float32
	Title         string
	AlreadyLinked bool
}

type Service struct {
	cfg          config.Config
	eva          *eva.Client
	embed        *embed.Client
	qd           *qdrant.Client
	log          *slog.Logger
	recreateDone bool
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
	if s.cfg.IndexerRecreate && !s.recreateDone {
		if err := s.qd.DropCollection(ctx, s.cfg.QdrantCollection); err != nil {
			s.log.Warn("drop collection", "err", err)
		}
		if err := s.EnsureCollection(ctx); err != nil {
			return err
		}
		s.recreateDone = true
	}
	existingScanStarted := time.Now()
	existing, err := s.qd.ExistingTaskIDs(ctx, s.cfg.QdrantCollection)
	if err != nil {
		return fmt.Errorf("read indexed tasks: %w", err)
	}
	s.log.Info("loaded indexed task IDs", "tasks", len(existing), "duration", time.Since(existingScanStarted))
	newCandidates := 0
	skipped := 0
	var indexedThisRun atomic.Int64
	var qdrantPointCount atomic.Int64
	initialPointCount, err := s.qd.CountPoints(ctx, s.cfg.QdrantCollection)
	if err != nil {
		return fmt.Errorf("count Qdrant points: %w", err)
	}
	qdrantPointCount.Store(int64(initialPointCount))
	var progressMu sync.Mutex
	var lastQdrantCountAt time.Time
	refreshQdrantCount := func(ctx context.Context, force bool) (int64, time.Time, error) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if !force && time.Since(lastQdrantCountAt) < 15*time.Second {
			return qdrantPointCount.Load(), lastQdrantCountAt, nil
		}
		count, err := s.qd.CountPoints(ctx, s.cfg.QdrantCollection)
		if err != nil {
			return qdrantPointCount.Load(), lastQdrantCountAt, err
		}
		lastQdrantCountAt = time.Now()
		pointCount := int64(count)
		qdrantPointCount.Store(pointCount)
		return pointCount, lastQdrantCountAt, nil
	}
	evaTotal, err := eva.CountTasks(ctx, s.eva, s.cfg.TaskListMethod, s.cfg.TaskListFilter)
	if err != nil {
		return fmt.Errorf("count Eva tasks: %w", err)
	}
	s.log.Info("index scan started", "tasks_in_qdrant", qdrantPointCount.Load(), "eva_total", evaTotal)
	const workers = 4
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan []map[string]any, workers)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var workErr error
	var failOnce sync.Once
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobs {
				indexed, err := s.indexRaw(workCtx, batch)
				if err != nil {
					failOnce.Do(func() {
						errMu.Lock()
						workErr = err
						errMu.Unlock()
						cancel()
					})
					continue
				}
				processed := indexedThisRun.Add(int64(indexed))
				qdrantCount, countedAt, countErr := refreshQdrantCount(workCtx, false)
				if countErr != nil {
					s.log.Warn("refresh Qdrant point count", "err", countErr)
				}
				s.log.Info("index progress",
					"tasks_in_qdrant", qdrantCount,
					"qdrant_count_updated_at", countedAt.Format(time.RFC3339),
					"tasks_indexed_this_run", processed,
					"eva_total", evaTotal,
				)
			}
		}()
	}
	page := 0
	var tasksSeen int64
	var scanErr error
	pageSize := s.cfg.IndexerPageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	batchSize := s.cfg.IndexerBatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	queue := func(batch []map[string]any) bool {
		select {
		case jobs <- batch:
			return true
		case <-workCtx.Done():
			return false
		}
	}
	pendingTasks := make([]map[string]any, 0, batchSize*workers)
	dispatch := func(fullBatchesOnly bool) bool {
		for len(pendingTasks) >= batchSize || (!fullBatchesOnly && len(pendingTasks) > 0) {
			n := batchSize
			if n > len(pendingTasks) {
				n = len(pendingTasks)
			}
			if !queue(pendingTasks[:n]) {
				return false
			}
			pendingTasks = pendingTasks[n:]
		}
		return true
	}
	for {
		if workCtx.Err() != nil {
			break
		}
		filter := s.cfg.TaskListFilter
		pageStarted := time.Now()
		tasks, _, err := eva.ListTasks(workCtx, s.eva, s.cfg.TaskListMethod, filter, s.cfg.TaskListFields, page*pageSize, pageSize)
		pageDuration := time.Since(pageStarted)
		if err != nil {
			s.log.Warn("Eva task page request failed", "offset", page*pageSize, "duration", pageDuration, "err", err)
			scanErr = err
			break
		}
		if len(tasks) == 0 {
			s.log.Info("read task page", "offset", page*pageSize, "page_size", 0,
				"tasks_seen", tasksSeen, "eva_total", evaTotal)
			break
		}
		tasksSeen += int64(len(tasks))
		newTasks := make([]map[string]any, 0, len(tasks))
		for _, task := range tasks {
			id := strp(lookup(task, s.cfg.TaskIDField))
			if id == "" {
				newTasks = append(newTasks, task)
				continue
			}
			if _, ok := existing[id]; ok {
				skipped++
				continue
			}
			newTasks = append(newTasks, task)
		}
		s.log.Info("read task page", "offset", page*pageSize, "page_size", len(tasks), "eva_request_duration", pageDuration,
			"new", len(newTasks), "tasks_seen", tasksSeen, "eva_total", evaTotal)
		pendingTasks = append(pendingTasks, newTasks...)
		// Wait until there is work for all workers before dispatching. This
		// avoids a single 100-task Eva page occupying just one worker.
		if len(pendingTasks) >= batchSize*workers && !dispatch(true) {
			break
		}
		for _, task := range newTasks {
			if id := strp(lookup(task, s.cfg.TaskIDField)); id != "" {
				existing[id] = struct{}{}
				newCandidates++
			}
		}
		if len(tasks) < pageSize {
			break
		}
		page++
	}
	if workCtx.Err() == nil {
		dispatch(false)
	}
	close(jobs)
	wg.Wait()
	errMu.Lock()
	workerErr := workErr
	errMu.Unlock()
	if workerErr != nil {
		return workerErr
	}
	if scanErr != nil {
		return scanErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, _, err := refreshQdrantCount(ctx, true); err != nil {
		s.log.Warn("final Qdrant point count refresh failed", "err", err)
	}
	s.log.Info("index scan complete",
		"tasks_in_qdrant", qdrantPointCount.Load(),
		"tasks_indexed_this_run", indexedThisRun.Load(),
		"eva_tasks_seen", tasksSeen,
		"eva_total", evaTotal,
		"new_candidates", newCandidates,
		"already_indexed", skipped,
	)
	return nil
}

// IndexRaw normalizes, chunks, embeds and upserts a batch of raw task maps.
func (s *Service) IndexRaw(ctx context.Context, tasks []map[string]any) error {
	_, err := s.indexRaw(ctx, tasks)
	return err
}

func (s *Service) indexRaw(ctx context.Context, tasks []map[string]any) (int, error) {
	prepareStarted := time.Now()
	docs := make([]*TaskDoc, 0, len(tasks))
	for _, raw := range tasks {
		doc, err := s.normalize(ctx, raw)
		if err != nil {
			s.log.Warn("normalize task", "err", err)
			continue
		}
		if len(doc.Chunks) == 0 {
			s.log.Warn("skip task without text", "id", doc.ID)
			continue
		}
		docs = append(docs, doc)
	}
	s.log.Info("task batch prepared", "tasks", len(tasks), "valid_tasks", len(docs), "duration", time.Since(prepareStarted))
	storeStarted := time.Now()
	if err := s.embedAndStore(ctx, docs); err != nil {
		return 0, err
	}
	s.log.Info("task batch indexed", "tasks", len(docs), "duration", time.Since(storeStarted))
	return len(docs), nil
}

// IndexTask embeds a single task by its id and stores it in Qdrant.
func (s *Service) IndexTask(ctx context.Context, id any) (*TaskDoc, error) {
	raw, err := eva.GetTask(ctx, s.eva, s.cfg.TaskGetMethod, s.cfg.TaskGetFilterField, id)
	if err != nil {
		return nil, err
	}
	doc, err := s.normalize(ctx, raw)
	if err != nil {
		return nil, err
	}
	if len(doc.Chunks) == 0 {
		return nil, fmt.Errorf("task %v has no text", id)
	}
	if err := s.embedAndStore(ctx, []*TaskDoc{doc}); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Service) embedAndStore(ctx context.Context, docs []*TaskDoc) error {
	type taskChunk struct {
		doc *TaskDoc
		idx int
	}
	var pending []taskChunk
	var texts []string
	flush := func() error {
		if len(texts) == 0 {
			return nil
		}
		embedStarted := time.Now()
		vectors, err := s.embed.Embed(ctx, texts)
		embedDuration := time.Since(embedStarted)
		if err != nil {
			s.log.Warn("embedding batch failed", "inputs", len(texts), "duration", embedDuration, "err", err)
			return err
		}
		if len(vectors) != len(pending) {
			return fmt.Errorf("embedding count mismatch: got %d, want %d", len(vectors), len(pending))
		}
		points := make([]qdrant.Point, 0, len(pending))
		for i, tc := range pending {
			d := tc.doc
			if len(d.ChunkVectors) <= tc.idx {
				d.ChunkVectors = append(d.ChunkVectors, make([][]float32, tc.idx+1-len(d.ChunkVectors))...)
			}
			d.ChunkVectors[tc.idx] = vectors[i]
			if d.Vector == nil {
				d.Vector = vectors[i]
			}
			points = append(points, s.chunkPoint(d, tc.idx, vectors[i], nil))
		}
		upsertStarted := time.Now()
		if err := s.qd.Upsert(ctx, s.cfg.QdrantCollection, points); err != nil {
			s.log.Warn("Qdrant upsert failed", "points", len(points), "duration", time.Since(upsertStarted), "err", err)
			return err
		}
		s.log.Info("index batch timings",
			"embedding_inputs", len(texts), "embedding_duration", embedDuration,
			"qdrant_points", len(points), "qdrant_upsert_duration", time.Since(upsertStarted),
		)
		s.log.Debug("stored", "count", len(points), "collection", s.cfg.QdrantCollection)
		pending = pending[:0]
		texts = texts[:0]
		return nil
	}
	for _, d := range docs {
		d.ChunkVectors = nil
		for i, text := range d.Chunks {
			pending = append(pending, taskChunk{d, i})
			texts = append(texts, text)
			batchSize := s.cfg.IndexerBatchSize
			if batchSize <= 0 {
				batchSize = 100
			}
			if len(texts) >= batchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	return flush()
}

// chunkPoint renders one Qdrant point for a task chunk. When keepLinks is
// non-nil it overrides the links stored in the payload.
func (s *Service) chunkPoint(d *TaskDoc, idx int, vector []float32, keepLinks []string) qdrant.Point {
	payload := make(map[string]any, len(d.Payload)+5)
	for k, v := range d.Payload {
		payload[k] = v
	}
	payload["eva_id"] = d.ID
	payload["title"] = d.Title
	if d.Code != "" {
		payload["code"] = d.Code
	}
	payload["chunk_index"] = idx
	payload["chunk_count"] = len(d.Chunks)
	if keepLinks != nil {
		payload["links"] = keepLinks
	} else if _, ok := payload["links"]; !ok {
		payload["links"] = []string{}
	}
	return qdrant.Point{ID: chunkPointID(d.ID, idx), Vector: vector, Payload: payload}
}

// chunkPointID hashes the task id + chunk index so every chunk of a task has
// its own stable Qdrant point id.
func chunkPointID(id string, idx int) uint64 {
	return qdrant.PointID(fmt.Sprintf("%s\x00%d", id, idx))
}

// FindSimilar searches Qdrant by every chunk of the query task and aggregates
// the best score per candidate task (a task can be hit from several chunks).
func (s *Service) FindSimilar(ctx context.Context, doc *TaskDoc) ([]Match, error) {
	self := make([]uint64, 0, len(doc.ChunkVectors))
	for i := range doc.ChunkVectors {
		self = append(self, chunkPointID(doc.ID, i))
	}
	best := map[string]*qdrant.ScoredPoint{}
	for _, v := range doc.ChunkVectors {
		hits, err := s.qd.Search(ctx, s.cfg.QdrantCollection, v, s.cfg.ListenerMaxLinks, s.cfg.ListenerScoreThreshold, self)
		if err != nil {
			return nil, err
		}
		for i := range hits {
			h := hits[i]
			id := str(h.Payload["eva_id"])
			if id == "" || id == doc.ID {
				continue
			}
			prev, ok := best[id]
			if !ok || h.Score > prev.Score {
				cp := h
				best[id] = &cp
			}
		}
	}
	all := make([]*qdrant.ScoredPoint, 0, len(best))
	for _, v := range best {
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > s.cfg.ListenerMaxLinks {
		all = all[:s.cfg.ListenerMaxLinks]
	}
	matches := make([]Match, 0, len(all))
	for _, h := range all {
		id := str(h.Payload["eva_id"])
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
			Code:          str(h.Payload["code"]),
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
			if err := eva.LinkTasks(ctx, s.eva, s.cfg.TaskLinkMethod, relID(doc.Code, doc.ID), relID(m.Code, m.ID), s.cfg.LinkRelationType); err != nil {
				s.log.Error("link failed", "from", doc.ID, "to", m.ID, "err", err)
				continue
			}
		}
		newLinks = append(newLinks, m.ID)
		s.log.Info("linked", "from", doc.ID, "to", m.ID, "score", m.Score, "dry_run", s.cfg.LinksDryRun)
	}

	if len(newLinks) > 0 {
		links := toStrings(doc.Payload["links"])
		for _, l := range newLinks {
			links = append(links, l)
		}
		doc.Payload["links"] = links
		points := make([]qdrant.Point, 0, len(doc.ChunkVectors))
		for i, v := range doc.ChunkVectors {
			points = append(points, s.chunkPoint(doc, i, v, links))
		}
		if err := s.qd.Upsert(ctx, s.cfg.QdrantCollection, points); err != nil {
			s.log.Warn("persist links", "err", err)
		}
	}
	return doc, matches, nil
}

// normalize converts a raw Eva task map into a TaskDoc using the configured
// field names (dotted paths allowed, e.g. "analysis.result") and chunks the
// text according to EMBED_CHUNKING.
func (s *Service) normalize(ctx context.Context, raw map[string]any) (*TaskDoc, error) {
	id := strp(lookup(raw, s.cfg.TaskIDField))
	if id == "" {
		return nil, fmt.Errorf("task has no id field %q", s.cfg.TaskIDField)
	}
	doc := &TaskDoc{
		ID:          id,
		Code:        strp(lookup(raw, s.cfg.TaskCodeField)),
		Title:       strp(lookup(raw, s.cfg.TaskTitleField)),
		Description: strp(lookup(raw, s.cfg.TaskDescField)),
		Result:      strp(lookup(raw, s.cfg.TaskResultField)),
	}
	in, err := eva.ListComments(ctx, s.eva, s.cfg.TaskCommentsMethod, s.cfg.TaskCommentParentPrefix, s.cfg.TaskCommentsFields, id)
	if err != nil {
		s.log.Warn("fetch comments", "task", id, "err", err)
	} else {
		for _, c := range in {
			t := textutil.Clean(strp(lookup(c, s.cfg.TaskCommentField)))
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
	doc.Chunks = s.buildChunks(doc)
	return doc, nil
}

// buildChunks splits the task material into embeddable units.
func (s *Service) buildChunks(d *TaskDoc) []string {
	if s.cfg.EmbedChunking == "none" {
		t := textutil.Join(s.cfg.EmbedMaxChars, d.Title, d.Description, d.Result, strings.Join(d.Comments, "\n"))
		if t == "" {
			return nil
		}
		return []string{t}
	}
	var chunks []string
	if t := textutil.Clean(d.Title); t != "" {
		chunks = append(chunks, "Заголовок: "+t)
	}
	chunks = append(chunks, s.semanticChunks("Описание", d.Description)...)
	chunks = append(chunks, s.semanticChunks("Результат", d.Result)...)
	chunks = append(chunks, s.commentChunks(d.Comments)...)
	return chunks
}

func (s *Service) buildText(d *TaskDoc) string {
	return textutil.Join(s.cfg.EmbedMaxChars, d.Title, d.Description, d.Result, strings.Join(d.Comments, "\n"))
}

// relID returns the identifier used as out_link/in_link in CmfRelationOption
// — the task code when present, otherwise its id.
func relID(code, id string) string {
	if code != "" {
		return code
	}
	return id
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

// toStrings normalizes a links payload value ([]string or []any) to []string.
func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, fmt.Sprint(e))
		}
		return out
	default:
		return nil
	}
}
