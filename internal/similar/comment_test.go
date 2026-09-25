package similar

import (
	"strings"
	"testing"
	"time"
)

func cmt(id, created string, link bool) map[string]any {
	m := map[string]any{"id": id, "text": "обычный комментарий", "cmf_created_at": created}
	if link {
		m["text"] = linkCommentMarker + " список"
	}
	return m
}

func TestPickLinkComment(t *testing.T) {
	base := "2026-09-25T08:00:00+03:00"
	ts := func(s string) time.Time {
		loc := time.FixedZone("t", 3*3600)
		t, _ := time.ParseInLocation(time.RFC3339Nano, s, loc)
		return t
	}

	t.Run("no comment", func(t *testing.T) {
		id, refresh := pickLinkComment(nil, "text", ts(base))
		if id != "" || !refresh {
			t.Fatalf("got (%q, %v), want (\"\", true)", id, refresh)
		}
	})

	t.Run("comment newer than task content", func(t *testing.T) {
		ids, refresh := pickLinkComment([]map[string]any{cmt("c1", base, true)}, "text", ts(base))
		if ids != "c1" || refresh {
			t.Fatalf("got (%q, %v), want (c1, false)", ids, refresh)
		}
	})

	t.Run("task modified after comment", func(t *testing.T) {
		c := "2026-09-25T08:00:00+03:00"
		ids, refresh := pickLinkComment([]map[string]any{cmt("c1", c, true)}, "text",
			ts("2026-09-25T08:30:00+03:00"))
		if ids != "c1" || !refresh {
			t.Fatalf("got (%q, %v), want (c1, true)", ids, refresh)
		}
	})

	t.Run("takes newest comment", func(t *testing.T) {
		rows := []map[string]any{cmt("old", "2026-09-25T07:00:00+03:00", true), cmt("new", base, true)}
		ids, refresh := pickLinkComment(rows, "text", ts("2026-09-25T09:00:00+03:00"))
		if ids != "new" {
			t.Fatalf("got id %q, want new", ids)
		}
		if !refresh {
			t.Fatalf("want refresh, got false")
		}
	})

	t.Run("ignores non-linker comments", func(t *testing.T) {
		rows := []map[string]any{cmt("plain", base, false)}
		ids, refresh := pickLinkComment(rows, "text", ts("2026-09-25T07:00:00+03:00"))
		if ids != "" || !refresh {
			t.Fatalf("got (%q, %v), want (\"\", true)", ids, refresh)
		}
	})
}

func TestContentFingerprint(t *testing.T) {
	base := []string{"а", "б"}
	// Order-insensitive over comments.
	if h1, h2 := contentFingerprint("t", "d", "r", base), contentFingerprint("t", "d", "r", []string{"б", "а"}); h1 != h2 {
		t.Fatalf("comment order must not change the fingerprint: %s vs %s", h1, h2)
	}
	// Any content change flips it.
	if h1, h2 := contentFingerprint("t", "d", "r", base), contentFingerprint("t", "d2", "r", base); h1 == h2 {
		t.Fatalf("description change must change the fingerprint")
	}
	if h1, h2 := contentFingerprint("t", "d", "r", base), contentFingerprint("t", "d", "r", nil); h1 == h2 {
		t.Fatalf("dropping comments must change the fingerprint")
	}
	// Deterministic.
	if contentFingerprint("t", "d", "r", base) != contentFingerprint("t", "d", "r", base) {
		t.Fatalf("fingerprint must be deterministic")
	}
}

func TestCommentText(t *testing.T) {
	matches := []Match{
		{ID: "CmfTask:000...1", Code: "SMAD-2271", Title: "BE: падение", Score: 0.9647427797317505},
		{ID: "CmfTask:000...2", Code: "", Title: "без кода", Score: 0.5},
	}
	text := CommentText("https://eva.staff.rfn.ru", matches)

	for _, want := range []string{
		linkCommentMarker,
		`data-mention-type="task"`,
		`data-object-id="CmfTask:000...1"`,
		`href="https://eva.staff.rfn.ru/project/Task/SMAD-2271"`,
		`<span class="card-name">SMAD-2271: BE: падение</span>`,
		`&amp;quot;viewMode&amp;quot;`,
		"0.9647",
		`CmfTask:000...2: без кода`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("comment text should contain %q, got: %s", want, text)
		}
	}
}

func TestCommentTextEscapes(t *testing.T) {
	m := []Match{{ID: "CmfTask:000...3", Code: "X-1", Title: `<b>bold</b> & "q"`, Score: 1}}
	text := CommentText("https://eva.staff.rfn.ru", m)
	if strings.Contains(text, "<script>") {
		t.Errorf("raw HTML must be escaped, got: %s", text)
	}
	if !strings.Contains(text, `<span class="card-name">X-1: &lt;b&gt;bold&lt;/b&gt; &amp; &#34;q&#34;</span>`) {
		t.Errorf("card-name must be escaped, got: %s", text)
	}
}

func TestCommentBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://eva.staff.rfn.ru/api":  "https://eva.staff.rfn.ru",
		"https://eva.staff.rfn.ru/api/": "https://eva.staff.rfn.ru",
		"http://localhost:8480":         "http://localhost:8480",
	} {
		if got := commentBaseURL(in); got != want {
			t.Errorf("commentBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapedAttrJSON(t *testing.T) {
	in := `{"viewMode":"text","title":"a & b"}`
	got := escapedAttrJSON(in)
	for _, want := range []string{`&amp;quot;viewMode&amp;quot;`, `a &amp; b`} {
		if !strings.Contains(got, want) {
			t.Errorf("escapedAttrJSON should contain %q, got: %s", want, got)
		}
	}
}
