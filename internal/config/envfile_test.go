package config

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadTaskCodeLists(t *testing.T) {
	t.Setenv("EVA_TASK_CODE_WHITELIST", "")
	t.Setenv("EVA_TASK_CODE_BLACKLIST", "")

	cfg := Load()
	if want := []string{"SMAD-", "SMOT-", "RED-"}; !reflect.DeepEqual(cfg.TaskCodeWhitelist, want) {
		t.Errorf("TaskCodeWhitelist = %v, want %v", cfg.TaskCodeWhitelist, want)
	}
	if want := []string{"SRE-"}; !reflect.DeepEqual(cfg.TaskCodeBlacklist, want) {
		t.Errorf("TaskCodeBlacklist = %v, want %v", cfg.TaskCodeBlacklist, want)
	}
}
