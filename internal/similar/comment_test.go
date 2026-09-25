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
		"https://eva.staff.rfn.ru/api": "https://eva.staff.rfn.ru",
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