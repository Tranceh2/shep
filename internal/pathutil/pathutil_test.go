package pathutil

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestExpandTilde is the canonical spec for tilde expansion shared across
// config, source, resolver and command. It pins the contract extracted from
// the four previously-duplicated helpers (one of which, command/doctor.go's
// copy, used home+p[1:] and was subtly wrong).
//
// Cannot run t.Parallel for the cases that mutate HOME.
func TestExpandTilde(t *testing.T) {
	home := t.TempDir()

	happy := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare tilde is home", in: "~", want: home},
		{name: "tilde slash one level", in: "~/code", want: filepath.Join(home, "code")},
		{name: "tilde slash nested", in: "~/a/b/c", want: filepath.Join(home, "a", "b", "c")},
		// Regression for the doctor.go bug: "~/" alone must collapse to home
		// via filepath.Join (no trailing separator), not home+"/".
		{name: "tilde slash only is home no trailing sep", in: "~/", want: home},
		{name: "relative path passes through", in: "relative", want: "relative"},
		{name: "absolute path passes through", in: "/abs/path", want: "/abs/path"},
		{name: "empty passes through", in: "", want: ""},
		{name: "tilde not at start passes through", in: "/x/~", want: "/x/~"},
		{name: "other tilde word passes through", in: "~other", want: "~other"},
	}

	for _, tc := range happy {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			got, err := ExpandTilde(tc.in)
			if err != nil {
				t.Fatalf("ExpandTilde(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ExpandTilde(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// A path with no leading tilde never needs HOME, so it must succeed even
	// when HOME is unresolvable.
	t.Run("non tilde succeeds without home", func(t *testing.T) {
		t.Setenv("HOME", "")
		got, err := ExpandTilde("/abs")
		if err != nil {
			t.Fatalf("ExpandTilde(%q): non-tilde must not need HOME: %v", "/abs", err)
		}
		if got != "/abs" {
			t.Errorf("ExpandTilde(%q) = %q, want %q", "/abs", got, "/abs")
		}
	})

	// A leading tilde DOES need HOME. On darwin os.UserHomeDir falls back to a
	// passwd lookup when HOME is unset, so an absent HOME may still resolve;
	// only assert the error off-darwin (mirrors internal/resolver's stance).
	t.Run("tilde errors when home unresolvable", func(t *testing.T) {
		t.Setenv("HOME", "")
		if _, err := ExpandTilde("~/x"); err == nil && runtime.GOOS != "darwin" {
			t.Errorf("ExpandTilde(%q): expected error without HOME on %s", "~/x", runtime.GOOS)
		}
	})
}

// TestExpandTilde_NoUserHomeDirLeak confirms a successful expansion lands
// inside the resolved home directory (not cwd or root), guarding against
// silent regressions to "/" or the process cwd when HOME is set.
func TestExpandTilde_NoUserHomeDirLeak(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ExpandTilde("~/projects/shep")
	if err != nil {
		t.Fatalf("ExpandTilde: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute expansion, got %q", got)
	}
	rel, err := filepath.Rel(home, got)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if want := filepath.Join("projects", "shep"); rel != want {
		t.Errorf("expansion = %q relative to home, want %q", rel, want)
	}
}

func TestStatePath(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "state")
	for _, tc := range []struct{ name, xdg, home, want string }{
		{"xdg", xdg, "", xdg},
		{"fallback", "", home, filepath.Join(home, ".local", "state")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", tc.xdg)
			t.Setenv("HOME", tc.home)
			got, err := StatePath("shep", "ranking.sqlite3")
			if err != nil || got != filepath.Join(tc.want, "shep", "ranking.sqlite3") {
				t.Fatalf("StatePath = %q, err=%v, want under %q", got, err, tc.want)
			}
		})
	}
}
