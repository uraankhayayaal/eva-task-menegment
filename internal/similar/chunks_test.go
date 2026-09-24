package similar

import (
	"strings"
	"testing"

	"evasimilar/internal/config"
	"evasimilar/internal/slogx"
)

func srv() *Service {
	cfg := config.Load()
	cfg.EmbedChunking = "semantic"
	cfg.EmbedChunkChars = 200
	return &Service{cfg: cfg, log: slogx.New("warn")}
}

func TestHTMLToSemanticBlocks(t *testing.T) {
	html := "<h2>Диагноз</h2><p>Текст абзаца один</p><p>Текст абзаца два</p><ul><li>Пункт один</li><li>Пункт два</li></ul>"
	blocks := htmlToSemanticBlocks(html)
	for _, b := range blocks {
		t.Logf("block: %q", b)
	}
	if len(blocks) != 5 {
		t.Fatalf("got %d blocks, want 5: %v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0], "# Диагноз") || !strings.HasPrefix(blocks[1], "Текст абзаца") || !strings.HasPrefix(blocks[3], "- Пункт") {
		t.Fatalf("unexpected structure: %v", blocks)
	}
}

func TestSemanticChunksMergesSmallBlocks(t *testing.T) {
	s := srv()
	text := "<p>Небольшой первый абзац</p><p>Небольшой второй абзац</p><p>Небольшой третий абзац</p>"
	chunks := s.semanticChunks("Описание", text)
	if len(chunks) != 1 {
		t.Fatalf("small blocks should merge into one chunk, got %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "Описание: ") || !strings.Contains(chunks[0], "третий") {
		t.Fatalf("labels missing: %q", chunks[0])
	}
	if len([]rune(chunks[0])) > s.cfg.EmbedChunkChars {
		t.Fatalf("chunk exceeds cap: %d", len([]rune(chunks[0])))
	}
}

func TestSplitOversizedBySentences(t *testing.T) {
	s := srv() // EmbedChunkChars = 200 here
	long := strings.Repeat("Очень длинное предложение без структуры. ", 10)
	chunks := splitOversized(long, s.cfg.EmbedChunkChars, 30)
	if len(chunks) < 2 {
		t.Fatalf("oversized text must split, got %d chunk(s)", len(chunks))
	}
	for _, c := range chunks {
		if len([]rune(c)) > s.cfg.EmbedChunkChars {
			t.Fatalf("chunk too long: %d runes", len([]rune(c)))
		}
	}
}

func TestCommentChunks(t *testing.T) {
	s := srv()
	comments := []string{"короче", "Ещё одна запись", "И третья короткая запись"}
	chunks := s.commentChunks(comments)
	if len(chunks) != 1 {
		t.Fatalf("comment group should be one chunk, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[0], "Комментарии: ") {
		t.Fatalf("label missing: %q", chunks[0])
	}
}
