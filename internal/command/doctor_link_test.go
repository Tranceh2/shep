package command

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
)

// runDoctorWithLink executes `shep doctor` against a fresh App wired to the
// in-memory link filesystem double, so the published-name report is exercised
// without touching the developer's real ~/.local/bin.
func runDoctorWithLink(t *testing.T, fsys linkFS, home, ownExe, pathVar string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	app := New(
		WithStreams(&out, &errOut),
		WithLinkFS(fsys),
		WithUserHomeDir(func() (string, error) { return home, nil }),
		WithLinkEnv(func(key string) string {
			if key == "PATH" {
				return pathVar
			}
			return ""
		}),
		WithExecutable(func() (string, error) { return ownExe, nil }),
		WithEvalSymlinks(func(p string) (string, error) { return p, nil }),
	)
	app.cfg = config.Defaults()
	app.cfg.Workspaces = nil
	app.probes = config.Probes{}
	app.themeGetenv = func(string) string { return "" }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor returned error: %v", err)
	}
	return out.String() + errOut.String()
}

func TestDoctor_ReportsPublishedNameState(t *testing.T) {
	t.Parallel()

	const (
		home   = "/home/u"
		ownExe = "/home/u/code/shep/bin/shep"
		linkAt = "/home/u/.local/bin/shep"
		onPath = "/usr/bin:/home/u/.local/bin"
	)

	tests := []struct {
		name string
		// setup preloads the destination state.
		setup func(*mockLinkFS)
		// wantAll must all appear in the output.
		wantAll []string
		// wantNone must not appear.
		wantNone []string
	}{
		{
			name:    "absent_reports_not_linked_and_names_the_verb",
			setup:   func(*mockLinkFS) {},
			wantAll: []string{linkAt, "not linked", "shep link"},
		},
		{
			name: "our_own_link_is_reported_as_this_install",
			setup: func(m *mockLinkFS) {
				m.addSymlink(linkAt, ownExe)
			},
			wantAll:  []string{"links to this install", ownExe},
			wantNone: []string{"DIFFERENT", "not linked"},
		},
		{
			// The whole reason this check exists: with several installs, a bare
			// `shep` reaches exactly one of them, and the operator cannot tell
			// which without being told.
			name: "another_install_is_called_out_explicitly",
			setup: func(m *mockLinkFS) {
				m.addSymlink(linkAt, "/opt/shep-v1/bin/shep")
			},
			wantAll: []string{
				"DIFFERENT shep install",
				"/opt/shep-v1/bin/shep",
				"shep link",
			},
		},
		{
			name: "foreign_symlink_is_reported_as_never_published",
			setup: func(m *mockLinkFS) {
				m.addSymlink(linkAt, "/usr/local/bin/some-tool")
			},
			wantAll:  []string{"/usr/local/bin/some-tool", "never published"},
			wantNone: []string{"DIFFERENT"},
		},
		{
			name: "regular_file_is_reported_as_not_replaceable",
			setup: func(m *mockLinkFS) {
				m.addFile(linkAt)
			},
			wantAll: []string{"a regular file", "will not replace"},
		},
		{
			name: "directory_is_reported_as_not_replaceable",
			setup: func(m *mockLinkFS) {
				m.addDir(linkAt)
			},
			wantAll: []string{"a directory", "will not replace"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := newMockLinkFS()
			tt.setup(fsys)

			got := runDoctorWithLink(t, fsys, home, ownExe, onPath)

			for _, want := range tt.wantAll {
				if !strings.Contains(got, want) {
					t.Fatalf("output missing %q\n--- output ---\n%s", want, got)
				}
			}
			for _, unwanted := range tt.wantNone {
				if strings.Contains(got, unwanted) {
					t.Fatalf("output unexpectedly contains %q\n--- output ---\n%s", unwanted, got)
				}
			}
		})
	}
}

// TestDoctor_NeverWritesWhileReporting is the invariant that makes this a
// diagnostic rather than a repair: doctor observes and never links, unlinks, or
// creates a directory, whatever it finds.
func TestDoctor_NeverWritesWhileReporting(t *testing.T) {
	t.Parallel()

	const (
		home   = "/home/u"
		ownExe = "/home/u/code/shep/bin/shep"
		linkAt = "/home/u/.local/bin/shep"
	)

	setups := map[string]func(*mockLinkFS){
		"absent":          func(*mockLinkFS) {},
		"foreign_file":    func(m *mockLinkFS) { m.addFile(linkAt) },
		"other_install":   func(m *mockLinkFS) { m.addSymlink(linkAt, "/opt/shep-v1/bin/shep") },
		"foreign_symlink": func(m *mockLinkFS) { m.addSymlink(linkAt, "/usr/local/bin/some-tool") },
	}

	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fsys := newMockLinkFS()
			setup(fsys)

			runDoctorWithLink(t, fsys, home, ownExe, "/usr/bin")

			if ops := fsys.writeOps(); len(ops) != 0 {
				t.Fatalf("doctor performed %d write operation(s), want 0: %v", len(ops), ops)
			}
		})
	}
}

func TestDoctor_PathNoteFollowsWhetherTheDirectoryIsOnPath(t *testing.T) {
	t.Parallel()

	const (
		home   = "/home/u"
		ownExe = "/home/u/code/shep/bin/shep"
	)

	tests := []struct {
		name     string
		pathVar  string
		wantNote bool
	}{
		{name: "absent_from_path_warns", pathVar: "/usr/bin:/bin", wantNote: true},
		{name: "present_on_path_is_silent", pathVar: "/usr/bin:/home/u/.local/bin", wantNote: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fsys := newMockLinkFS()
			fsys.addSymlink("/home/u/.local/bin/shep", ownExe)

			got := runDoctorWithLink(t, fsys, home, ownExe, tt.pathVar)

			hasNote := strings.Contains(got, "is not on your PATH")
			if hasNote != tt.wantNote {
				t.Fatalf("PATH note present = %v, want %v\n--- output ---\n%s", hasNote, tt.wantNote, got)
			}
		})
	}
}

// TestDoctor_UnresolvableOwnPathIsNotReportedAsAMismatch pins the distinction
// between two different answers: "this name points at another install" is a
// finding, while "we cannot tell which install we are" is an unknown. Reporting
// the second as the first would send the operator chasing a problem that may
// not exist.
func TestDoctor_UnresolvableOwnPathIsNotReportedAsAMismatch(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	fsys.addSymlink("/home/u/.local/bin/shep", "/home/u/code/shep/bin/shep")

	var out, errOut bytes.Buffer
	app := New(
		WithStreams(&out, &errOut),
		WithLinkFS(fsys),
		WithUserHomeDir(func() (string, error) { return "/home/u", nil }),
		WithLinkEnv(func(string) string { return "" }),
		WithExecutable(func() (string, error) { return "", errors.New("exe unavailable") }),
	)
	app.cfg = config.Defaults()
	app.cfg.Workspaces = nil
	app.probes = config.Probes{}
	app.themeGetenv = func(string) string { return "" }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor returned error: %v", err)
	}

	got := out.String() + errOut.String()
	if !strings.Contains(got, "cannot compare with this install") {
		t.Fatalf("output should report the comparison as unknown\n--- output ---\n%s", got)
	}
	if strings.Contains(got, "DIFFERENT") {
		t.Fatalf("an unresolvable own path must not be reported as a mismatch\n--- output ---\n%s", got)
	}
}

// TestDoctor_UnresolvableHomeIsReportedNotFatal keeps doctor a diagnostic that
// always completes: an unresolvable home makes the published name unknown, not
// a command failure.
func TestDoctor_UnresolvableHomeIsReportedNotFatal(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer
	app := New(
		WithStreams(&out, &errOut),
		WithLinkFS(newMockLinkFS()),
		WithUserHomeDir(func() (string, error) { return "", errors.New("no home") }),
		WithLinkEnv(func(string) string { return "" }),
	)
	app.cfg = config.Defaults()
	app.cfg.Workspaces = nil
	app.probes = config.Probes{}
	app.themeGetenv = func(string) string { return "" }
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor must not fail on an unresolvable home: %v", err)
	}

	got := out.String() + errOut.String()
	if !strings.Contains(got, "unknown") || !strings.Contains(got, "no home") {
		t.Fatalf("output should report the published name as unknown\n--- output ---\n%s", got)
	}
}
