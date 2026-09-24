package similar

import (
	"html"
	"regexp"
	"strings"
)

var (
	semHeadOpenRe  = regexp.MustCompile(`(?i)<h[1-6][^>]*>`)
	semHeadCloseRe = regexp.MustCompile(`(?i)</h[1-6]>`)
	semLiOpenRe    = regexp.MustCompile(`(?i)<li[^>]*>`)
	semEndBlockRe  = regexp.MustCompile(`(?i)</(p|div|section|article|blockquote|li|tr|table|ul|ol|dl)>`)
	semBrRe        = regexp.MustCompile(`(?i)<br\s*/?>`)
	semTagRe       = regexp.MustCompile(`(?s)<[^>]+>`)
	semWsRe        = regexp.MustCompile(`[ \t]+`)
	semSentRe      = regexp.MustCompile(`.+?(?:[.!?…]+(?:\s+|$)|;\s+|\n|$)`)
)

// htmlToSemanticBlocks turns task HTML (or plain text) into semantic units:
// headings, paragraphs and list items become separate blocks, preserving a
// "# " / "- " marker so the embedding keeps the structure readable.
func htmlToSemanticBlocks(s string) []string {
	s = semHeadOpenRe.ReplaceAllString(s, "\n# ")
	s = semHeadCloseRe.ReplaceAllString(s, "\n")
	s = semLiOpenRe.ReplaceAllString(s, "\n- ")
	s = semEndBlockRe.ReplaceAllString(s, "\n")
	s = semBrRe.ReplaceAllString(s, "\n")
	s = semTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = semWsRe.ReplaceAllString(s, " ")
	var blocks []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			blocks = append(blocks, line)
		}
	}
	return blocks
}

// semanticChunks splits one task section (description/result) into chunks:
// small semantic blocks are greedily merged up to EmbedChunkChars; a single
// oversized block is cut sentence-by-sentence with an overlap window.
func (s *Service) semanticChunks(label, text string) []string {
	blocks := htmlToSemanticBlocks(text)
	if len(blocks) == 0 {
		return nil
	}
	if label != "" {
		blocks[0] = label + ": " + blocks[0]
	}
	maxChars := s.cfg.EmbedChunkChars
	overlap := s.cfg.EmbedChunkOverlap
	if maxChars <= 0 {
		maxChars = 2000
	}
	var out []string
	var cur []string
	curLen := 0
	flush := func() {
		if curLen > 0 {
			out = append(out, strings.Join(cur, "\n"))
		}
		cur = nil
		curLen = 0
	}
	for _, b := range blocks {
		n := len([]rune(b))
		if n >= maxChars {
			flush()
			out = append(out, splitOversized(b, maxChars, overlap)...)
			continue
		}
		if curLen > 0 && curLen+n+1 > maxChars {
			flush()
		}
		cur = append(cur, b)
		curLen += n + 1
	}
	flush()
	return out
}

// commentChunks groups a task's comments into chunks of up to EmbedChunkChars
// runes, keeping each comment's text intact.
func (s *Service) commentChunks(comments []string) []string {
	maxChars := s.cfg.EmbedChunkChars
	if maxChars <= 0 {
		maxChars = 2000
	}
	var out []string
	var cur []string
	curLen := 0
	flush := func() {
		if curLen > 0 {
			out = append(out, strings.Join(cur, "\n"))
		}
		cur = nil
		curLen = 0
	}
	for _, c := range comments {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		n := len([]rune(c))
		if curLen > 0 && curLen+n+1 > maxChars {
			flush()
		}
		cur = append(cur, c)
		curLen += n + 1
	}
	flush()
	if len(out) > 0 {
		out[0] = "Комментарии: " + out[0]
	}
	return out
}

// splitOversized cuts a block that alone exceeds maxChars. It first splits by
// sentence boundaries and packs whole sentences into chunks; a single
// sentence that still exceeds maxChars is hard-split with an overlap.
func splitOversized(text string, maxChars, overlap int) []string {
	if maxChars < 1 {
		maxChars = 1
	}
	step := maxChars - overlap
	if step < 1 {
		step = maxChars
	}
	var sents []string
	for _, m := range semSentRe.FindAllString(text, -1) {
		if t := strings.TrimSpace(m); t != "" {
			sents = append(sents, t)
		}
	}
	var chunks []string
	var buf strings.Builder
	bufLen := 0
	for _, sn := range sents {
		n := len([]rune(sn))
		if n > maxChars {
			if bufLen > 0 {
				chunks = append(chunks, strings.TrimSpace(buf.String()))
				buf.Reset()
				bufLen = 0
			}
			r := []rune(sn)
			for i := 0; i < len(r); i += step {
				end := i + maxChars
				if end > len(r) {
					end = len(r)
				}
				chunks = append(chunks, strings.TrimSpace(string(r[i:end])))
			}
			continue
		}
		if bufLen > 0 && bufLen+n+1 > maxChars {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
			buf.Reset()
			bufLen = 0
		}
		if bufLen > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(sn)
		bufLen += n + 1
	}
	if bufLen > 0 {
		chunks = append(chunks, strings.TrimSpace(buf.String()))
	}
	return chunks
}
