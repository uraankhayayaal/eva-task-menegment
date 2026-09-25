package similar

import (
	"strings"
	"testing"
)

func TestCommentText(t *testing.T) {
	matches := []Match{
		{ID: "CmfTask:000...1", Code: "SMAD-2271", Title: "BE: падение", Score: 0.9647427797317505},
		{ID: "CmfTask:000...2", Code: "", Title: "без кода", Score: 0.5},
	}
	text := CommentText(matches)

	for _, want := range []string{linkCommentMarker, "SMAD-2271", "BE: падение", "0.9647", "CmfTask:000...2"} {
		if !strings.Contains(text, want) {
			t.Errorf("comment text should contain %q, got: %s", want, text)
		}
	}
	if strings.Contains(text, "<script>") {
		t.Error("comment text should escape HTML")
	}
}

func TestCommentTextEscapes(t *testing.T) {
	m := []Match{{Code: `"><script>`, Title: `<b>bold</b>`, Score: 1}}
	text := CommentText(m)
	if strings.Contains(text, "<script>") {
		t.Errorf("raw HTML must be escaped, got: %s", text)
	}
	if !strings.Contains(text, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Errorf("title must be escaped, got: %s", text)
	}
}