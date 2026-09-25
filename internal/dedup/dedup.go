package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"evasimilar/internal/qdrant"
)

// Duplicate is one group of points sharing the same eva_id + chunk_index.
// KeepID is the point that stays; DupIDs are the points to delete.
type Duplicate struct {
	EvaID      string
	ChunkIndex int
	KeepID     uint64
	DupIDs     []uint64
}

// Result summarizes one dedup run over a collection.
type Result struct {
	Collection string
	Scanned    int // points read from Qdrant
	Tasks      int // distinct eva_ids seen
	DupGroups  int // groups with at least one duplicate
	DupPoints  int // points that would be / were deleted
	Deleted    int // points actually deleted (0 without apply)
	Duplicates []Duplicate
	Errs       []string
}

// Run scrolls the whole collection, groups points by eva_id + chunk_index and,
// when apply is true, deletes all duplicates keeping one point per group (the
// one with the smallest id). Without apply it only reports.
func Run(ctx context.Context, q *qdrant.Client, name string, apply bool, log *slog.Logger) (*Result, error) {
	res := &Result{Collection: name}
	points, err := q.Scroll(ctx, name, []string{"eva_id", "chunk_index"})
	if err != nil {
		return res, err
	}
	res.Scanned = len(points)
	collect(points, res)

	if res.DupPoints == 0 {
		return res, nil
	}
	if !apply {
		return res, nil
	}

	const batchSize = 1000
	var all []uint64
	for _, d := range res.Duplicates {
		all = append(all, d.DupIDs...)
	}
	deleted := 0
	for i := 0; i < len(all); i += batchSize {
		end := i + batchSize
		if end > len(all) {
			end = len(all)
		}
		batch := all[i:end]
		if err := q.DeleteByIDs(ctx, name, batch); err != nil {
			res.Errs = append(res.Errs, fmt.Sprintf("points %d..%d: %v", i, end-1, err))
			continue
		}
		deleted += len(batch)
		if log != nil {
			log.Info("delete batch", "collection", name, "points", len(batch), "deleted", deleted)
		}
	}
	res.Deleted = deleted
	return res, nil
}

// collect groups ScrolledPoints by eva_id + chunk_index and fills the Result.
// Points without a usable eva_id are ignored. In each group the point with the
// smallest id becomes the keeper, all others become DupIDs.
func collect(points []qdrant.ScrolledPoint, res *Result) {
	type group struct {
		evaID string
		idx   int
		ids   []uint64
	}
	groups := map[string]*group{}
	var order []string
	tasks := map[string]struct{}{}
	for _, p := range points {
		evaID := fmt.Sprint(p.Payload["eva_id"])
		if evaID == "" || evaID == "<nil>" {
			continue
		}
		idx, _ := toInt(p.Payload["chunk_index"])
		tasks[evaID] = struct{}{}
		key := evaID + "\x00" + strconv.Itoa(idx)
		g, ok := groups[key]
		if !ok {
			g = &group{evaID: evaID, idx: idx}
			groups[key] = g
			order = append(order, key)
		}
		g.ids = append(g.ids, p.ID)
	}
	res.Tasks = len(tasks)
	for _, key := range order {
		g := groups[key]
		if len(g.ids) <= 1 {
			continue
		}
		sort.Slice(g.ids, func(i, j int) bool { return g.ids[i] < g.ids[j] })
		d := Duplicate{EvaID: g.evaID, ChunkIndex: g.idx, KeepID: g.ids[0], DupIDs: g.ids[1:]}
		res.Duplicates = append(res.Duplicates, d)
		res.DupGroups++
		res.DupPoints += len(d.DupIDs)
	}
}

// toInt converts a JSON payload scalar into an int, tolerating the number
// shapes Qdrant returns (float64, json.Number) and string values.
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		n, err := strconv.Atoi(t)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
