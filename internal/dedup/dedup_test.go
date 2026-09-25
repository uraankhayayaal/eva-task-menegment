package dedup

import (
	"encoding/json"
	"testing"

	"evasimilar/internal/qdrant"
)

type pt struct {
	id    uint64
	evaID string
	idx   int
}

func mkPoints(entries ...pt) []qdrant.ScrolledPoint {
	out := make([]qdrant.ScrolledPoint, 0, len(entries))
	for _, e := range entries {
		out = append(out, qdrant.ScrolledPoint{
			ID:      e.id,
			Payload: map[string]any{"eva_id": e.evaID, "chunk_index": e.idx},
		})
	}
	return out
}

func TestCollect(t *testing.T) {
	pts := mkPoints(
		pt{10, "a", 0},
		pt{11, "a", 0},
		pt{12, "a", 1},
		pt{30, "b", 0},
		pt{31, "b", 0},
		pt{32, "b", 0},
	)

	res := &Result{}
	collect(pts, res)

	if res.Tasks != 2 {
		t.Fatalf("tasks=%d, want 2", res.Tasks)
	}
	if res.DupGroups != 2 || res.DupPoints != 3 {
		t.Fatalf("dup_groups=%d dup_points=%d, want 2 and 3", res.DupGroups, res.DupPoints)
	}
	// group "a"\x000: keep 10, delete 11; "b"\x000: keep 30, delete 31,32.
	found := false
	for _, d := range res.Duplicates {
		switch d.EvaID {
		case "a":
			found = true
			if d.KeepID != 10 || len(d.DupIDs) != 1 || d.DupIDs[0] != 11 {
				t.Fatalf("bad group %+v", d)
			}
		case "b":
			if d.KeepID != 30 || len(d.DupIDs) != 2 || d.DupIDs[0] != 31 || d.DupIDs[1] != 32 {
				t.Fatalf("bad group %+v", d)
			}
		}
	}
	if !found {
		t.Fatal("missing group for eva_id=a chunk 0")
	}
}

func TestCollectNoDuplicates(t *testing.T) {
	pts := mkPoints(
		pt{1, "x", 3},
		pt{2, "x", 4},
	)
	res := &Result{}
	collect(pts, res)
	if res.DupGroups != 0 || res.DupPoints != 0 || res.Tasks != 1 {
		t.Fatalf("expected no duplicates, got groups=%d points=%d tasks=%d", res.DupGroups, res.DupPoints, res.Tasks)
	}
}

func TestCollectSkipsInvalidEvaID(t *testing.T) {
	pts := mkPoints(
		pt{1, "", 0},
		pt{2, "<nil>", 0},
		pt{3, "", 0},
	)
	res := &Result{}
	collect(pts, res)
	if res.Tasks != 0 || res.DupGroups != 0 {
		t.Fatalf("invalid eva_ids must be skipped, got tasks=%d groups=%d", res.Tasks, res.DupGroups)
	}
}

func TestToInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
		ok   bool
	}{
		{float64(7), 7, true},
		{int(7), 7, true},
		{"7", 7, true},
		{json.Number("7"), 7, true},
		{nil, 0, false},
		{true, 0, false},
	}
	for _, c := range cases {
		got, ok := toInt(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("toInt(%v) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
