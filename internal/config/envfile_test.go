package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	content := `# comment
EVA_API_TOKEN=secret-token
OLLAMA_MODEL = "nomic-embed-text"  # inline comment
EMPTY=
export QUOTED='abc def'
REAL=from-file
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Setenv("REAL", "from-env") // environment must win

	if err := LoadEnvFile(p); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		key, want string
	}{
		{"EVA_API_TOKEN", "secret-token"},
		{"OLLAMA_MODEL", "nomic-embed-text"},
		{"QUOTED", "abc def"},
		{"EMPTY", ""},
		{"REAL", "from-env"}, // not overridden
	}
	for _, c := range cases {
		if got := os.Getenv(c.key); got != c.want {
			t.Errorf("%s = %q, want %q", c.key, got, c.want)
		}
		os.Unsetenv(c.key)
	}
}
