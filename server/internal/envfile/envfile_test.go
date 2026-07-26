package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# comment
OPENAI_API_KEY=sk-secret123

export OPENAI_MODEL=deepseek-v4-flash
QUOTED="quoted value"
SINGLE='single value'
SPACED = spaced
TRAILING=value # inline comment
EMPTY=
NOEQUALS
ALREADY_SET=from-file
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALREADY_SET", "from-env")

	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, want string }{
		{"OPENAI_API_KEY", "sk-secret123"},
		{"OPENAI_MODEL", "deepseek-v4-flash"},
		{"QUOTED", "quoted value"},
		{"SINGLE", "single value"},
		{"SPACED", "spaced"},
		{"TRAILING", "value"},
		{"EMPTY", ""},
		{"ALREADY_SET", "from-env"}, // real env wins
	} {
		if got := os.Getenv(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
	if _, ok := os.LookupEnv("NOEQUALS"); ok {
		t.Error("NOEQUALS should not be set")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("missing file returned %v, want nil", err)
	}
}
