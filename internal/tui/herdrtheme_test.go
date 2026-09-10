package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withHerdrConfigPath temporarily overrides herdrConfigPathFn for the duration of the test.
func withHerdrConfigPath(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := herdrConfigPathFn
	herdrConfigPathFn = fn
	t.Cleanup(func() { herdrConfigPathFn = orig })
}

func TestHerdrThemeName_Matrix(t *testing.T) {
	tests := []struct {
		name      string
		toml      string
		prepFile  func(t *testing.T, path string)
		wantName  string
		wantOK    bool
		pathErr   error
		checkLeak bool
	}{
		{
			name:     "valid mocha",
			toml:     "[theme]\nname = \"mocha\"\n",
			wantName: ThemeMocha,
			wantOK:   true,
		},
		{
			name:     "valid latte",
			toml:     "[theme]\nname = \"latte\"\n",
			wantName: ThemeLatte,
			wantOK:   true,
		},
		{
			name:     "valid macchiato",
			toml:     "[theme]\nname = \"macchiato\"\n",
			wantName: ThemeMacchiato,
			wantOK:   true,
		},
		{
			name:     "valid frappe",
			toml:     "[theme]\nname = \"frappe\"\n",
			wantName: ThemeFrappe,
			wantOK:   true,
		},
		{
			name:     "valid plain",
			toml:     "[theme]\nname = \"plain\"\n",
			wantName: ThemePlain,
			wantOK:   true,
		},
		{
			name:     "unrecognized flavourless catppuccin falls through per #7144",
			toml:     "[theme]\nname = \"catppuccin\"\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "unrecognized arbitrary theme name",
			toml:     "[theme]\nname = \"solarized-dark\"\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "empty theme table",
			toml:     "[theme]\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "missing file",
			toml:     "",
			wantName: "",
			wantOK:   false,
			prepFile: func(t *testing.T, path string) {
				// remove file so it does not exist
				_ = os.Remove(path)
			},
		},
		{
			name:     "permission denied",
			toml:     "[theme]\nname = \"latte\"\n",
			wantName: "",
			wantOK:   false,
			prepFile: func(t *testing.T, path string) {
				if os.Geteuid() == 0 {
					t.Skip("skipping EPERM test as root")
				}
				if err := os.Chmod(path, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			},
		},
		{
			name:     "malformed TOML syntax",
			toml:     "[[theme\nname = invalid\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "oversized file exceeding 1 MiB limit",
			wantName: "",
			wantOK:   false,
			prepFile: func(t *testing.T, path string) {
				// Create a file > 1 MiB with valid header but padded past 1 MiB
				var buf bytes.Buffer
				buf.WriteString("[theme]\nname = \"latte\"\n")
				buf.WriteString("# " + strings.Repeat("A", 1024*1024+128) + "\n")
				if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "hostile shell command in theme name is inert",
			toml:     "[theme]\nname = \"$(rm -rf /)\"\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "hostile backtick in theme name is inert",
			toml:     "[theme]\nname = \"`touch /tmp/bad`\"\n",
			wantName: "",
			wantOK:   false,
		},
		{
			name:      "secret environment variable is not leaked",
			toml:      "[theme]\nname = \"mocha\"\n",
			wantName:  ThemeMocha,
			wantOK:    true,
			checkLeak: true,
		},
		{
			name:     "herdrConfigPathFn returns error",
			wantName: "",
			wantOK:   false,
			pathErr:  fmt.Errorf("config path error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgPath := filepath.Join(tmpDir, "config.toml")

			if tt.pathErr != nil {
				withHerdrConfigPath(t, func() (string, error) {
					return "", tt.pathErr
				})
			} else {
				if tt.toml != "" {
					if err := os.WriteFile(cfgPath, []byte(tt.toml), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if tt.prepFile != nil {
					tt.prepFile(t, cfgPath)
				}
				withHerdrConfigPath(t, func() (string, error) {
					return cfgPath, nil
				})
			}

			if tt.checkLeak {
				t.Setenv("SECRET_SHEP_TOKEN", "super-secret-token-12345")
			}

			gotName, gotOK := herdrThemeName()
			if gotOK != tt.wantOK {
				t.Errorf("herdrThemeName() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotName != tt.wantName {
				t.Errorf("herdrThemeName() name = %q, want %q", gotName, tt.wantName)
			}
			if tt.checkLeak && strings.Contains(gotName, "super-secret") {
				t.Errorf("herdrThemeName() leaked secret into result: %q", gotName)
			}
		})
	}
}
