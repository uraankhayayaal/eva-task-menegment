package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadEnvFile reads a dotenv-style file and injects the variables into the
// process environment. Variables already present in the environment win and
// are never overridden. It is a no-op when the file does not exist.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" || !isEnvName(key) {
			continue
		}
		val := parseValue(strings.TrimSpace(line[eq+1:]))
		if _, ok := os.LookupEnv(key); ok {
			continue // real environment wins
		}
		os.Setenv(key, val)
	}
	return sc.Err()
}

// parseValue strips inline comments and surrounding quotes.
func parseValue(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func isEnvName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
