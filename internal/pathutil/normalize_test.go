package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNormalize_Table is the consolidated spec for Normalize, covering the
// same cases previously pinned separately by internal/resolver's
// TestNormalize_Table and internal/herdr's normalizePath usage: tilde
// expansion, absolute resolution, trailing-slash trimming, dot cleanup and
// that an unresolvable symlink still returns a stable, non-empty key.
func TestNormalize_Table(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// A symlink whose target does not exist: EvalSymlinks fails to resolve
	// it, so Normalize must fall back to the cleaned, un-resolved path
	// instead of erroring — the dangling target is never followed.
	brokenTarget := filepath.Join(tmp, "does-not-exist-target")
	brokenLink := filepath.Join(tmp, "broken")
	if err := os.Symlink(brokenTarget, brokenLink); err != nil {
		t.Fatal(err)
	}
	// A path that does not exist at all (no symlink involved): EvalSymlinks
	// still fails (no such file), same fallback applies.
	missing := "/this/path/does/not/exist/anywhere"

	cases := []struct {
		name    string
		input   string
		wantAbs bool
		wantSub string
		wantErr bool
	}{
		{name: "empty errors", input: "", wantErr: true},
		{name: "absolute with trailing slash trimmed", input: target + string(filepath.Separator), wantSub: target},
		{name: "symlink resolves to target", input: link, wantSub: target},
		{name: "relative resolved to absolute", input: ".", wantAbs: true},
		{name: "dot cleanup", input: target + "/./sub/..", wantSub: target},
		{name: "broken symlink falls back to cleaned path", input: brokenLink, wantAbs: true, wantSub: brokenLink},
		{name: "nonexistent path falls back to cleaned path", input: missing, wantAbs: true, wantSub: missing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize %q: %v", tc.input, err)
			}
			if tc.wantAbs && !filepath.IsAbs(got) {
				t.Errorf("expected absolute, got %q", got)
			}
			if tc.wantSub != "" && !strings.Contains(got, tc.wantSub) {
				t.Errorf("expected result to contain %q, got %q", tc.wantSub, got)
			}
			if strings.HasSuffix(got, string(filepath.Separator)) && got != string(filepath.Separator) {
				t.Errorf("result has trailing separator: %q", got)
			}
		})
	}
}

// TestNormalize_TildeExpansion confirms Normalize expands a leading ~ before
// making the path absolute — the one behavior herdr's pre-consolidation
// normalizePath duplicate silently lacked (its real inputs were always
// already-absolute pane CWDs, so the gap never surfaced there).
func TestNormalize_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Normalize("~/project")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := filepath.Join(home, "project")
	if got != want {
		t.Errorf("Normalize(~/project) = %q, want %q", got, want)
	}
}

// TestNormalize_HomeUnresolvable returns an error when HOME is unavailable so
// determination cannot silently produce a wrong dedup key.
func TestNormalize_HomeUnresolvable(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := Normalize("~/x"); err == nil && runtime.GOOS != "darwin" {
		t.Error("expected error expanding ~ without HOME on non-darwin")
	}
}
