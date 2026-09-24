package textutil

import (
	"html"
	"regexp"
	"strings"
)

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRe    = regexp.MustCompile(`\s+`)
	enDashR = regexp.MustCompile(`[\p{Cc}]+`)
)

// Clean strips HTML tags and entities, collapses whitespace.
func Clean(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// Truncate limits s to max runes.
func Truncate(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// Join builds the embedding source text: title + description + result + comments.
func Join(max int, parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		p = Clean(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p)
	}
	s := b.String()
	return Truncate(s, max)
}

// StripControl removes non-printable runes (safe for JSON/HTTP bodies).
func StripControl(s string) string {
	return enDashR.ReplaceAllString(s, "")
}
