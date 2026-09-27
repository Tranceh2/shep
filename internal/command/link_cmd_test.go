package command

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type mockEntry struct {
	isDir     bool
	isSymlink bool
	isFile    bool
	target    string
}

type mockLinkFS struct {
	mu         sync.Mutex
	entries    map[string]mockEntry
	operations []string
	mkdirErr   error
	symlinkErr error
	removeErr  error
}

func newMockLinkFS() *mockLinkFS {
	return &mockLinkFS{
		entries: make(map[string]mockEntry),
	}
}

func (m *mockLinkFS) addFile(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[filepath.Clean(path)] = mockEntry{isFile: true}
}

func (m *mockLinkFS) addDir(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[filepath.Clean(path)] = mockEntry{isDir: true}
}

func (m *mockLinkFS) addSymlink(path, target string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[filepath.Clean(path)] = mockEntry{isSymlink: true, target: target}
}

func (m *mockLinkFS) Probe(path string) linkProbe {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	e, ok := m.entries[cleanPath]
	if !ok {
		return linkProbe{Kind: probeAbsent}
	}
	if e.isSymlink {
		return linkProbe{
			Kind:   probeSymlink,
			Target: resolveLinkTarget(path, e.target),
		}
	}
	if e.isDir {
		return linkProbe{Kind: probeOther, What: "a directory"}
	}
	return linkProbe{Kind: probeOther, What: "a regular file"}
}

func (m *mockLinkFS) MkdirAll(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mkdirErr != nil {
		return m.mkdirErr
	}
	cleanDir := filepath.Clean(dir)
	m.operations = append(m.operations, "mkdir:"+cleanDir)
	m.entries[cleanDir] = mockEntry{isDir: true}
	return nil
}

func (m *mockLinkFS) Symlink(target, at string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.symlinkErr != nil {
		return m.symlinkErr
	}
	cleanAt := filepath.Clean(at)
	m.operations = append(m.operations, fmt.Sprintf("symlink:%s->%s", target, cleanAt))
	m.entries[cleanAt] = mockEntry{isSymlink: true, target: target}
	return nil
}

func (m *mockLinkFS) Remove(at string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.removeErr != nil {
		return m.removeErr
	}
	cleanAt := filepath.Clean(at)
	m.operations = append(m.operations, "remove:"+cleanAt)
	delete(m.entries, cleanAt)
	return nil
}

func (m *mockLinkFS) Exists(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[filepath.Clean(path)]
	return ok
}

func (m *mockLinkFS) writeOps() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]string, len(m.operations))
	copy(copied, m.operations)
	return copied
}

func (m *mockLinkFS) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.operations)
}

func defaultTestApp(fsys linkFS, out, errOut *bytes.Buffer, env map[string]string) *App {
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	return New(
		WithStreams(out, errOut),
		WithLinkFS(fsys),
		WithUserHomeDir(func() (string, error) { return "/home/alice", nil }),
		WithLinkEnv(func(key string) string {
			if env != nil {
				if v, ok := env[key]; ok {
					return v
				}
			}
			if key == "PATH" {
				return "/home/alice/.local/bin:/usr/bin:/bin"
			}
			return ""
		}),
		WithExecutable(func() (string, error) { return ownBinary, nil }),
		WithEvalSymlinks(func(p string) (string, error) { return p, nil }),
	)
}

func TestLink_CreateIntoEmptyDir(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	fsys.addFile(ownBinary)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	wantOps := []string{
		"mkdir:/home/alice/.local/bin",
		"symlink:" + ownBinary + "->/home/alice/.local/bin/shep",
	}
	gotOps := fsys.writeOps()
	if len(gotOps) != len(wantOps) {
		t.Fatalf("got ops %v, want %v", gotOps, wantOps)
	}
	for i := range wantOps {
		if gotOps[i] != wantOps[i] {
			t.Errorf("op[%d] got %q, want %q", i, gotOps[i], wantOps[i])
		}
	}

	wantOut := "linked /home/alice/.local/bin/shep -> " + ownBinary + "\n"
	if out.String() != wantOut {
		t.Fatalf("got output %q, want %q", out.String(), wantOut)
	}
}

func TestLink_KeepWhenAlreadyCorrect(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(ownBinary)
	fsys.addSymlink(dest, ownBinary)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on linkKeep, got %d ops: %v", count, fsys.writeOps())
	}

	wantSub := dest + " already links to " + ownBinary
	if !strings.Contains(out.String(), wantSub) {
		t.Fatalf("output %q does not contain %q", out.String(), wantSub)
	}
}

func TestLink_ReplaceAnotherShepInstall(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-new/bin/shep"
	oldBinary := "/home/alice/.config/herdr/plugins/shep-old/bin/shep"
	dest := "/home/alice/.local/bin/shep"

	fsys.addFile(ownBinary)
	fsys.addSymlink(dest, oldBinary)

	var out, errOut bytes.Buffer
	app := New(
		WithStreams(&out, &errOut),
		WithLinkFS(fsys),
		WithUserHomeDir(func() (string, error) { return "/home/alice", nil }),
		WithLinkEnv(func(key string) string {
			if key == "PATH" {
				return "/home/alice/.local/bin:/usr/bin"
			}
			return ""
		}),
		WithExecutable(func() (string, error) { return ownBinary, nil }),
		WithEvalSymlinks(func(p string) (string, error) { return p, nil }),
	)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	wantOps := []string{
		"mkdir:/home/alice/.local/bin",
		"remove:" + dest,
		"symlink:" + ownBinary + "->" + dest,
	}
	gotOps := fsys.writeOps()
	if len(gotOps) != len(wantOps) {
		t.Fatalf("got ops %v, want %v", gotOps, wantOps)
	}
	for i := range wantOps {
		if gotOps[i] != wantOps[i] {
			t.Errorf("op[%d] got %q, want %q", i, gotOps[i], wantOps[i])
		}
	}

	if !strings.Contains(out.String(), oldBinary) {
		t.Fatalf("output %q does not name previous target %q", out.String(), oldBinary)
	}
	if !strings.Contains(out.String(), ownBinary) {
		t.Fatalf("output %q does not name new target %q", out.String(), ownBinary)
	}
}

func TestLink_RefuseRegularFile(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(ownBinary)
	fsys.addFile(dest)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on refusing regular file, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on refuse, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), dest) {
		t.Fatalf("errOut %q does not name destination %q", errOut.String(), dest)
	}
	if !strings.Contains(errOut.String(), "a regular file") {
		t.Fatalf("errOut %q does not mention reason 'a regular file'", errOut.String())
	}
	if !strings.Contains(errOut.String(), "move it aside") {
		t.Fatalf("errOut %q does not instruct user to move it aside", errOut.String())
	}
}

func TestLink_RefuseDirectory(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(ownBinary)
	fsys.addDir(dest)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on refusing directory, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on refuse, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), dest) {
		t.Fatalf("errOut %q does not name destination %q", errOut.String(), dest)
	}
	if !strings.Contains(errOut.String(), "a directory") {
		t.Fatalf("errOut %q does not mention reason 'a directory'", errOut.String())
	}
	if !strings.Contains(errOut.String(), "move it aside") {
		t.Fatalf("errOut %q does not instruct user to move it aside", errOut.String())
	}
}

func TestLink_RefuseForeignSymlink(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	dest := "/home/alice/.local/bin/shep"
	foreignTarget := "/usr/local/bin/ripgrep"
	fsys.addFile(ownBinary)
	fsys.addSymlink(dest, foreignTarget)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on refusing foreign symlink, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on refuse, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), dest) {
		t.Fatalf("errOut %q does not name destination %q", errOut.String(), dest)
	}
	if !strings.Contains(errOut.String(), foreignTarget) {
		t.Fatalf("errOut %q does not name foreign target %q", errOut.String(), foreignTarget)
	}
	if !strings.Contains(errOut.String(), "move it aside") {
		t.Fatalf("errOut %q does not instruct user to move it aside", errOut.String())
	}
}

func TestLink_PATHNote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pathEnv  string
		wantNote bool
	}{
		{
			name:     "link dir absent from PATH",
			pathEnv:  "/usr/bin:/bin",
			wantNote: true,
		},
		{
			name:     "link dir present on PATH",
			pathEnv:  "/home/alice/.local/bin:/usr/bin:/bin",
			wantNote: false,
		},
		{
			name:     "link dir present with trailing slash",
			pathEnv:  "/home/alice/.local/bin/:/usr/bin:/bin",
			wantNote: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fsys := newMockLinkFS()
			ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
			fsys.addFile(ownBinary)

			var out, errOut bytes.Buffer
			app := defaultTestApp(fsys, &out, &errOut, map[string]string{"PATH": tt.pathEnv})
			cmd := app.rootCmd()
			cmd.SetArgs([]string{"link"})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("link failed: %v", err)
			}

			hasNote := strings.Contains(out.String(), "is not on your PATH")
			if hasNote != tt.wantNote {
				t.Fatalf("got wantNote=%v, but output was %q", hasNote, out.String())
			}
		})
	}
}

func TestLink_EnvOverrides(t *testing.T) {
	t.Parallel()

	t.Run("SHEP_LINK_DIR override", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		fsys.addFile(ownBinary)

		var out, errOut bytes.Buffer
		app := defaultTestApp(fsys, &out, &errOut, map[string]string{
			"SHEP_LINK_DIR": "/custom/bin",
			"PATH":          "/custom/bin",
		})
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("link failed: %v", err)
		}

		wantOps := []string{
			"mkdir:/custom/bin",
			"symlink:" + ownBinary + "->/custom/bin/shep",
		}
		gotOps := fsys.writeOps()
		if len(gotOps) != len(wantOps) {
			t.Fatalf("got ops %v, want %v", gotOps, wantOps)
		}
		for i := range wantOps {
			if gotOps[i] != wantOps[i] {
				t.Errorf("op[%d] got %q, want %q", i, gotOps[i], wantOps[i])
			}
		}
	})

	t.Run("XDG_BIN_HOME override", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		fsys.addFile(ownBinary)

		var out, errOut bytes.Buffer
		app := defaultTestApp(fsys, &out, &errOut, map[string]string{
			"XDG_BIN_HOME": "/xdg/bin",
			"PATH":         "/xdg/bin",
		})
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		if err := cmd.Execute(); err != nil {
			t.Fatalf("link failed: %v", err)
		}

		wantOps := []string{
			"mkdir:/xdg/bin",
			"symlink:" + ownBinary + "->/xdg/bin/shep",
		}
		gotOps := fsys.writeOps()
		if len(gotOps) != len(wantOps) {
			t.Fatalf("got ops %v, want %v", gotOps, wantOps)
		}
		for i := range wantOps {
			if gotOps[i] != wantOps[i] {
				t.Errorf("op[%d] got %q, want %q", i, gotOps[i], wantOps[i])
			}
		}
	})
}

func TestLink_SelfLinkRefusal(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(dest)

	var out, errOut bytes.Buffer
	app := New(
		WithStreams(&out, &errOut),
		WithLinkFS(fsys),
		WithUserHomeDir(func() (string, error) { return "/home/alice", nil }),
		WithLinkEnv(func(string) string { return "" }),
		WithExecutable(func() (string, error) { return dest, nil }),
		WithEvalSymlinks(func(p string) (string, error) { return p, nil }),
	)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on self-link, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on self-link refusal, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), "cannot link /home/alice/.local/bin/shep to itself") {
		t.Fatalf("errOut %q does not contain expected self-link message", errOut.String())
	}
}

func TestUnlink_Absent(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	fsys.addFile(ownBinary)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"unlink"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unlink failed: %v", err)
	}

	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on unlink absent, got %d: %v", count, fsys.writeOps())
	}
	wantOut := "/home/alice/.local/bin/shep is not linked\n"
	if out.String() != wantOut {
		t.Fatalf("got output %q, want %q", out.String(), wantOut)
	}
}

func TestUnlink_RemovesOwnLink(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(ownBinary)
	fsys.addSymlink(dest, ownBinary)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"unlink"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unlink failed: %v", err)
	}

	wantOps := []string{"remove:" + dest}
	gotOps := fsys.writeOps()
	if len(gotOps) != len(wantOps) || gotOps[0] != wantOps[0] {
		t.Fatalf("got ops %v, want %v", gotOps, wantOps)
	}

	if !strings.Contains(out.String(), "removed "+dest) {
		t.Fatalf("output %q does not confirm removal of %q", out.String(), dest)
	}
	if !strings.Contains(out.String(), "is untouched") {
		t.Fatalf("output %q does not mention untouched install", out.String())
	}
}

func TestUnlink_RefusesAnotherInstallLink(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	otherBinary := "/home/alice/.config/herdr/plugins/shep-other/bin/shep"
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(ownBinary)
	fsys.addSymlink(dest, otherBinary)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"unlink"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on unlinking foreign shep link, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on refuse, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), dest) {
		t.Fatalf("errOut %q does not name destination %q", errOut.String(), dest)
	}
	if !strings.Contains(errOut.String(), otherBinary) {
		t.Fatalf("errOut %q does not name owning install %q", errOut.String(), otherBinary)
	}
}

func TestUnlink_RefusesForeignFileOrSymlink(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
	foreignTarget := "/usr/bin/python3"
	dest := "/home/alice/.local/bin/shep"
	fsys.addFile(ownBinary)
	fsys.addSymlink(dest, foreignTarget)

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"unlink"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on unlinking foreign symlink, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on refuse, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), dest) {
		t.Fatalf("errOut %q does not name destination %q", errOut.String(), dest)
	}
	if !strings.Contains(errOut.String(), foreignTarget) {
		t.Fatalf("errOut %q does not name foreign target %q", errOut.String(), foreignTarget)
	}
}

func TestLink_BinaryMissing(t *testing.T) {
	t.Parallel()

	fsys := newMockLinkFS()
	// ownBinary is not added to fsys.

	var out, errOut bytes.Buffer
	app := defaultTestApp(fsys, &out, &errOut, nil)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"link"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when binary is missing, got nil")
	}
	if !errors.Is(err, errExitOne) {
		t.Fatalf("got err = %v, want errExitOne", err)
	}
	if count := fsys.writeCount(); count != 0 {
		t.Fatalf("expected 0 writes on missing binary, got %d: %v", count, fsys.writeOps())
	}
	if !strings.Contains(errOut.String(), "binary not found") {
		t.Fatalf("errOut %q does not mention missing binary", errOut.String())
	}
}

func TestLink_FSErrorsSurfaceAsWrappedErrors(t *testing.T) {
	t.Parallel()

	t.Run("MkdirAll error in link", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		fsys.addFile(ownBinary)
		fsys.mkdirErr = errors.New("mkdir permission denied")

		var out, errOut bytes.Buffer
		app := defaultTestApp(fsys, &out, &errOut, nil)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error from MkdirAll, got nil")
		}
		if !strings.Contains(err.Error(), "mkdir permission denied") {
			t.Fatalf("got error %v, want wrapped mkdir error", err)
		}
	})

	t.Run("Symlink error in link", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		fsys.addFile(ownBinary)
		fsys.symlinkErr = errors.New("symlink disk full")

		var out, errOut bytes.Buffer
		app := defaultTestApp(fsys, &out, &errOut, nil)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error from Symlink, got nil")
		}
		if !strings.Contains(err.Error(), "symlink disk full") {
			t.Fatalf("got error %v, want wrapped symlink error", err)
		}
	})

	t.Run("Remove error in link replace", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		oldBinary := "/home/alice/.config/herdr/plugins/shep-old/bin/shep"
		dest := "/home/alice/.local/bin/shep"
		fsys.addFile(ownBinary)
		fsys.addSymlink(dest, oldBinary)
		fsys.removeErr = errors.New("remove read-only filesystem")

		var out, errOut bytes.Buffer
		app := defaultTestApp(fsys, &out, &errOut, nil)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error from Remove, got nil")
		}
		if !strings.Contains(err.Error(), "remove read-only filesystem") {
			t.Fatalf("got error %v, want wrapped remove error", err)
		}
	})

	t.Run("Remove error in unlink", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		dest := "/home/alice/.local/bin/shep"
		fsys.addFile(ownBinary)
		fsys.addSymlink(dest, ownBinary)
		fsys.removeErr = errors.New("remove permission denied")

		var out, errOut bytes.Buffer
		app := defaultTestApp(fsys, &out, &errOut, nil)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"unlink"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error from Remove, got nil")
		}
		if !strings.Contains(err.Error(), "remove permission denied") {
			t.Fatalf("got error %v, want wrapped remove error", err)
		}
	})
}

func TestLink_ResolutionFailures(t *testing.T) {
	t.Parallel()

	t.Run("executable resolution error", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		var out, errOut bytes.Buffer
		app := New(
			WithStreams(&out, &errOut),
			WithLinkFS(fsys),
			WithExecutable(func() (string, error) { return "", errors.New("no executable") }),
		)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, errExitOne) {
			t.Fatalf("got err = %v, want errExitOne", err)
		}
		if !strings.Contains(errOut.String(), "no executable") {
			t.Fatalf("errOut %q does not contain cause", errOut.String())
		}
	})

	t.Run("home dir resolution error", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		ownBinary := "/home/alice/.config/herdr/plugins/shep-1234/bin/shep"
		fsys.addFile(ownBinary)

		var out, errOut bytes.Buffer
		app := New(
			WithStreams(&out, &errOut),
			WithLinkFS(fsys),
			WithExecutable(func() (string, error) { return ownBinary, nil }),
			WithEvalSymlinks(func(p string) (string, error) { return p, nil }),
			WithUserHomeDir(func() (string, error) { return "", errors.New("no home dir") }),
		)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"link"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, errExitOne) {
			t.Fatalf("got err = %v, want errExitOne", err)
		}
		if !strings.Contains(errOut.String(), "no home dir") {
			t.Fatalf("errOut %q does not contain cause", errOut.String())
		}
	})

	t.Run("unlink executable resolution error", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		var out, errOut bytes.Buffer
		app := New(
			WithStreams(&out, &errOut),
			WithLinkFS(fsys),
			WithExecutable(func() (string, error) { return "", errors.New("no executable") }),
		)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"unlink"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, errExitOne) {
			t.Fatalf("got err = %v, want errExitOne", err)
		}
	})

	t.Run("unlink home resolution error", func(t *testing.T) {
		t.Parallel()

		fsys := newMockLinkFS()
		var out, errOut bytes.Buffer
		app := New(
			WithStreams(&out, &errOut),
			WithLinkFS(fsys),
			WithExecutable(func() (string, error) { return "/bin/shep", nil }),
			WithEvalSymlinks(func(p string) (string, error) { return p, nil }),
			WithUserHomeDir(func() (string, error) { return "", errors.New("no home dir") }),
		)
		cmd := app.rootCmd()
		cmd.SetArgs([]string{"unlink"})

		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, errExitOne) {
			t.Fatalf("got err = %v, want errExitOne", err)
		}
	})
}

func TestRealLinkFS(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	rfs := realLinkFS{}

	// Non-existent path
	absentPath := filepath.Join(tmp, "absent")
	if probe := rfs.Probe(absentPath); probe.Kind != probeAbsent {
		t.Fatalf("got kind %v, want probeAbsent", probe.Kind)
	}
	if rfs.Exists(absentPath) {
		t.Fatal("expected Exists to return false for absent path")
	}

	// Create directory
	subDir := filepath.Join(tmp, "subdir")
	if err := rfs.MkdirAll(subDir); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if probe := rfs.Probe(subDir); probe.Kind != probeOther || probe.What != "a directory" {
		t.Fatalf("got probe %+v, want probeOther 'a directory'", probe)
	}

	// Create regular file
	filePath := filepath.Join(tmp, "regular.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if !rfs.Exists(filePath) {
		t.Fatal("expected Exists to return true for existing file")
	}
	if probe := rfs.Probe(filePath); probe.Kind != probeOther || probe.What != "a regular file" {
		t.Fatalf("got probe %+v, want probeOther 'a regular file'", probe)
	}

	// Create symlink
	symlinkPath := filepath.Join(tmp, "symlink-to-file")
	if err := rfs.Symlink(filePath, symlinkPath); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}
	if probe := rfs.Probe(symlinkPath); probe.Kind != probeSymlink || probe.Target != filePath {
		t.Fatalf("got probe %+v, want probeSymlink to %s", probe, filePath)
	}

	// Remove symlink
	if err := rfs.Remove(symlinkPath); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if probe := rfs.Probe(symlinkPath); probe.Kind != probeAbsent {
		t.Fatalf("got probe %+v, want probeAbsent after remove", probe)
	}
}
