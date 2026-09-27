package command

import (
	"path/filepath"
	"strings"
	"testing"
)

// envMap builds a linkEnv over a fixed map so a developer's real SHEP_LINK_DIR
// or XDG_BIN_HOME can never change what these tests assert.
func envMap(pairs map[string]string) linkEnv {
	return func(key string) string { return pairs[key] }
}

func TestLinkDir_PrecedenceAndAbsoluteOnly(t *testing.T) {
	t.Parallel()

	const home = "/home/u"

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "defaults_to_local_bin_under_home",
			env:  nil,
			want: filepath.Join(home, ".local", "bin"),
		},
		{
			name: "xdg_bin_home_wins_over_default",
			env:  map[string]string{"XDG_BIN_HOME": "/opt/xdgbin"},
			want: "/opt/xdgbin",
		},
		{
			name: "shep_link_dir_wins_over_xdg",
			env: map[string]string{
				"SHEP_LINK_DIR": "/opt/override",
				"XDG_BIN_HOME":  "/opt/xdgbin",
			},
			want: "/opt/override",
		},
		{
			// A relative override must be ignored, not resolved against the
			// cwd: where a PATH name lives cannot depend on the directory the
			// verb happened to run from.
			name: "relative_override_is_ignored",
			env:  map[string]string{"SHEP_LINK_DIR": "relative/bin"},
			want: filepath.Join(home, ".local", "bin"),
		},
		{
			name: "relative_xdg_falls_through_to_default",
			env:  map[string]string{"XDG_BIN_HOME": "also/relative"},
			want: filepath.Join(home, ".local", "bin"),
		},
		{
			name: "blank_override_falls_through_to_xdg",
			env: map[string]string{
				"SHEP_LINK_DIR": "   ",
				"XDG_BIN_HOME":  "/opt/xdgbin",
			},
			want: "/opt/xdgbin",
		},
		{
			name: "trailing_separator_is_cleaned",
			env:  map[string]string{"SHEP_LINK_DIR": "/opt/override/"},
			want: "/opt/override",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := linkDir(home, envMap(tt.env)); got != tt.want {
				t.Fatalf("linkDir = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLinkPath_IsShepInsideLinkDir(t *testing.T) {
	t.Parallel()

	got := linkPath("/home/u", envMap(nil))
	want := filepath.Join("/home/u", ".local", "bin", "shep")
	if got != want {
		t.Fatalf("linkPath = %q, want %q", got, want)
	}
}

func TestResolveLinkTarget_MakesRelativeAbsoluteAgainstLinkDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		linkAt string
		raw    string
		want   string
	}{
		{
			name:   "absolute_target_is_kept",
			linkAt: "/home/u/.local/bin/shep",
			raw:    "/opt/shep/bin/shep",
			want:   "/opt/shep/bin/shep",
		},
		{
			name:   "relative_target_resolves_against_the_links_own_directory",
			linkAt: "/home/u/.local/bin/shep",
			raw:    "../../code/shep/bin/shep",
			want:   "/home/u/code/shep/bin/shep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveLinkTarget(tt.linkAt, tt.raw); got != tt.want {
				t.Fatalf("resolveLinkTarget = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsShepBinaryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{name: "checkout_binary", target: "/home/u/code/shep/bin/shep", want: true},
		{name: "herdr_managed_hashed_dir", target: "/home/u/.config/herdr/plugins/github/tranceh2.shep-1edf0e1e/bin/shep", want: true},
		{
			// The guard exists to stop `link` from taking over a name that is
			// not ours. A same-named binary in a directory that is not `bin`
			// was not published by Shep.
			name:   "shep_outside_a_bin_directory",
			target: "/usr/lib/shep/shep",
			want:   false,
		},
		{name: "another_tool_in_a_bin_directory", target: "/opt/other/bin/collie", want: false},
		{name: "similar_prefix_is_not_a_match", target: "/opt/shep/bin/shep-helper", want: false},
		{name: "empty", target: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isShepBinaryPath(tt.target); got != tt.want {
				t.Fatalf("isShepBinaryPath(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

func TestClassifyLink(t *testing.T) {
	t.Parallel()

	const own = "/home/u/code/shep/bin/shep"

	tests := []struct {
		name         string
		probe        linkProbe
		wantAction   linkAction
		wantPrevious string
		reasonHas    string
	}{
		{
			name:       "absent_destination_is_created",
			probe:      linkProbe{Kind: probeAbsent},
			wantAction: linkCreate,
		},
		{
			name:       "already_ours_is_a_no_op",
			probe:      linkProbe{Kind: probeSymlink, Target: own},
			wantAction: linkKeep,
		},
		{
			name:         "another_shep_install_is_taken_over",
			probe:        linkProbe{Kind: probeSymlink, Target: "/opt/shep-v1/bin/shep"},
			wantAction:   linkReplace,
			wantPrevious: "/opt/shep-v1/bin/shep",
		},
		{
			// A foreign symlink must be refused untouched, and the refusal has
			// to name what it found so the operator can judge it.
			name:       "foreign_symlink_is_refused_and_named",
			probe:      linkProbe{Kind: probeSymlink, Target: "/usr/local/bin/some-tool"},
			wantAction: linkRefuse,
			reasonHas:  "/usr/local/bin/some-tool",
		},
		{
			name:       "regular_file_is_refused",
			probe:      linkProbe{Kind: probeOther, What: "a regular file"},
			wantAction: linkRefuse,
			reasonHas:  "a regular file",
		},
		{
			name:       "directory_is_refused",
			probe:      linkProbe{Kind: probeOther, What: "a directory"},
			wantAction: linkRefuse,
			reasonHas:  "a directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyLink(tt.probe, own)
			if got.Action != tt.wantAction {
				t.Fatalf("action = %v, want %v", got.Action, tt.wantAction)
			}
			if tt.wantPrevious != "" && got.Previous != tt.wantPrevious {
				t.Fatalf("previous = %q, want %q", got.Previous, tt.wantPrevious)
			}
			if tt.reasonHas != "" && !strings.Contains(got.Reason, tt.reasonHas) {
				t.Fatalf("reason = %q, want it to contain %q", got.Reason, tt.reasonHas)
			}
		})
	}
}

func TestClassifyUnlink(t *testing.T) {
	t.Parallel()

	const own = "/home/u/code/shep/bin/shep"

	tests := []struct {
		name       string
		probe      linkProbe
		wantAction unlinkAction
		reasonHas  string
	}{
		{
			name:       "absent_is_reported_not_failed",
			probe:      linkProbe{Kind: probeAbsent},
			wantAction: unlinkAbsent,
		},
		{
			name:       "our_own_link_is_removed",
			probe:      linkProbe{Kind: probeSymlink, Target: own},
			wantAction: unlinkRemove,
		},
		{
			// Stricter than link on purpose: another install's link is that
			// install's to remove, so unlink refuses rather than taking it down.
			name:       "another_shep_install_link_is_refused",
			probe:      linkProbe{Kind: probeSymlink, Target: "/opt/shep-v1/bin/shep"},
			wantAction: unlinkRefuse,
			reasonHas:  "that install owns the name",
		},
		{
			name:       "foreign_symlink_is_refused",
			probe:      linkProbe{Kind: probeSymlink, Target: "/usr/local/bin/some-tool"},
			wantAction: unlinkRefuse,
			reasonHas:  "shep never published",
		},
		{
			name:       "regular_file_is_refused",
			probe:      linkProbe{Kind: probeOther, What: "a regular file"},
			wantAction: unlinkRefuse,
			reasonHas:  "a regular file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyUnlink(tt.probe, own)
			if got.Action != tt.wantAction {
				t.Fatalf("action = %v, want %v", got.Action, tt.wantAction)
			}
			if tt.reasonHas != "" && !strings.Contains(got.Reason, tt.reasonHas) {
				t.Fatalf("reason = %q, want it to contain %q", got.Reason, tt.reasonHas)
			}
		})
	}
}

// TestClassifyLink_ReplaceAndUnlinkRefuseAgreeOnForeignInstalls pins the
// asymmetry itself, because it is a deliberate design decision rather than an
// accident: `link` may take a foreign Shep install's name over, while `unlink`
// must refuse the very same destination.
func TestClassifyLink_ReplaceAndUnlinkRefuseAgreeOnForeignInstalls(t *testing.T) {
	t.Parallel()

	const own = "/home/u/code/shep/bin/shep"
	foreign := linkProbe{Kind: probeSymlink, Target: "/opt/shep-v1/bin/shep"}

	if got := classifyLink(foreign, own).Action; got != linkReplace {
		t.Fatalf("link action = %v, want linkReplace", got)
	}
	if got := classifyUnlink(foreign, own).Action; got != unlinkRefuse {
		t.Fatalf("unlink action = %v, want unlinkRefuse", got)
	}
}

func TestOnPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dir     string
		pathVar string
		want    bool
	}{
		{name: "present", dir: "/home/u/.local/bin", pathVar: "/usr/bin:/home/u/.local/bin:/bin", want: true},
		{name: "absent", dir: "/home/u/.local/bin", pathVar: "/usr/bin:/bin", want: false},
		{
			// The shell compares strings, so a trailing separator must not make
			// an otherwise-present directory read as missing.
			name:    "trailing_separator_still_matches",
			dir:     "/home/u/.local/bin",
			pathVar: "/usr/bin:/home/u/.local/bin/",
			want:    true,
		},
		{name: "empty_path_var", dir: "/home/u/.local/bin", pathVar: "", want: false},
		{name: "empty_entries_are_skipped", dir: "/home/u/.local/bin", pathVar: "::/home/u/.local/bin", want: true},
		{name: "prefix_is_not_a_match", dir: "/home/u/.local/bin", pathVar: "/home/u/.local/binary", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := onPath(tt.dir, tt.pathVar); got != tt.want {
				t.Fatalf("onPath(%q, %q) = %v, want %v", tt.dir, tt.pathVar, got, tt.want)
			}
		})
	}
}
