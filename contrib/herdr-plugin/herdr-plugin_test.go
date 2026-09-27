// Package herdrplugin holds the Herdr plugin manifest for Shep.
// The tests here verify the manifest contract against Herdr requirements.
package herdrplugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

type manifest struct {
	ID              string     `toml:"id"`
	Name            string     `toml:"name"`
	Version         string     `toml:"version"`
	MinHerdrVersion string     `toml:"min_herdr_version"`
	Description     string     `toml:"description"`
	Platforms       []string   `toml:"platforms"`
	Build           []buildCmd `toml:"build"`
	Startup         []startup  `toml:"startup"`
	Panes           []pane     `toml:"panes"`
	Actions         []action   `toml:"actions"`
	Events          []event    `toml:"events"`
}

type buildCmd struct {
	Command []string `toml:"command"`
}

type startup struct {
	Command   []string `toml:"command"`
	Platforms []string `toml:"platforms"`
}

type pane struct {
	ID        string   `toml:"id"`
	Title     string   `toml:"title"`
	Placement string   `toml:"placement"`
	Width     string   `toml:"width"`
	Height    string   `toml:"height"`
	Command   []string `toml:"command"`
}

type action struct {
	ID          string   `toml:"id"`
	Title       string   `toml:"title"`
	Description string   `toml:"description"`
	Contexts    []string `toml:"contexts"`
	Command     []string `toml:"command"`
}

type event struct {
	On      string   `toml:"on"`
	Command []string `toml:"command"`
}

const manifestPath = "herdr-plugin.toml"

func loadManifest(t *testing.T) manifest {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	var m manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	return m
}

func TestManifest_ParsesWithRequiredHerdrMetadata(t *testing.T) {
	m := loadManifest(t)

	if m.ID != "tranceh2.shep" {
		t.Fatalf("id = %q, want tranceh2.shep", m.ID)
	}
	if m.Name != "Shep" {
		t.Fatalf("name = %q, want Shep", m.Name)
	}
	if m.Version != "0.1.1" {
		t.Fatalf("version = %q, want 0.1.1", m.Version)
	}
	if m.MinHerdrVersion != "0.8.2" {
		t.Fatalf("min_herdr_version = %q, want 0.8.2", m.MinHerdrVersion)
	}
	if len(m.Platforms) == 0 {
		t.Fatal("platforms must be declared")
	}
	wantPlatforms := []string{"linux", "macos"}
	if !equalSlice(m.Platforms, wantPlatforms) {
		t.Fatalf("platforms = %v, want %v", m.Platforms, wantPlatforms)
	}
	for _, p := range m.Platforms {
		if p == "windows" {
			t.Fatal("windows must not be declared")
		}
	}
}

func TestManifest_ExactlyOneBuildStepCompilingBinary(t *testing.T) {
	m := loadManifest(t)

	if len(m.Build) != 1 {
		t.Fatalf("build steps = %d, want exactly 1", len(m.Build))
	}
	want := []string{"bash", "scripts/build.sh"}
	if !equalSlice(m.Build[0].Command, want) {
		t.Fatalf("build command = %v, want %v", m.Build[0].Command, want)
	}
}

func TestManifest_BuildsFromHerdrPluginWorkingDirectory(t *testing.T) {
	m := loadManifest(t)
	if len(m.Build) != 1 {
		t.Fatalf("build steps = %d, want exactly 1", len(m.Build))
	}

	repoRoot := repositoryRoot(t)
	disposableRoot := t.TempDir()
	copyTrackedBuildInputs(t, repoRoot, disposableRoot)

	pluginRoot := filepath.Join(disposableRoot, "contrib", "herdr-plugin")
	cmd := exec.Command(m.Build[0].Command[0], m.Build[0].Command[1:]...)
	cmd.Dir = pluginRoot
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build command %v failed from plugin cwd: %v\n%s", m.Build[0].Command, err, output)
	}

	binaryPath := filepath.Join(pluginRoot, "bin", "shep")
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatalf("stat plugin binary: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("plugin binary mode = %v, want executable", info.Mode())
	}

	versionOutput := runBinary(t, binaryPath, "--version")
	if strings.TrimSpace(versionOutput) == "" || strings.Contains(versionOutput, "commit: )") {
		t.Fatalf("version output has empty version or commit metadata: %q", versionOutput)
	}
	if !strings.Contains(versionOutput, "commit: ") {
		t.Fatalf("version output = %q, want commit metadata", versionOutput)
	}

	if _, err := os.Stat(filepath.Join(disposableRoot, "bin", "shep")); !os.IsNotExist(err) {
		t.Fatalf("binary was written outside plugin root: err = %v", err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func copyTrackedBuildInputs(t *testing.T, repoRoot, disposableRoot string) {
	t.Helper()
	paths := []string{"cmd", "internal", "go.mod", "go.sum", "contrib/herdr-plugin/herdr-plugin.toml", "contrib/herdr-plugin/scripts/build.sh"}
	for _, relativePath := range paths {
		sourcePath := filepath.Join(repoRoot, relativePath)
		destinationPath := filepath.Join(disposableRoot, relativePath)
		if err := copyPath(sourcePath, destinationPath); err != nil {
			t.Fatalf("copy %s: %v", relativePath, err)
		}
	}
}

func copyPath(sourcePath, destinationPath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.MkdirAll(destinationPath, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(sourcePath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyPath(filepath.Join(sourcePath, entry.Name()), filepath.Join(destinationPath, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destinationPath, data, info.Mode().Perm())
}

func runBinary(t *testing.T, binaryPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %v: %v\n%s", binaryPath, args, err, output)
	}
	if len(output) == 0 {
		t.Fatalf("run %s %v produced no output", binaryPath, args)
	}
	return string(output)
}

func TestManifest_ExactlyOneStartupHookRunningCollector(t *testing.T) {
	m := loadManifest(t)

	if len(m.Startup) != 1 {
		t.Fatalf("startup hooks = %d, want exactly 1", len(m.Startup))
	}
	want := []string{"bash", "scripts/run-shep.sh", "watch-history"}
	if !equalSlice(m.Startup[0].Command, want) {
		t.Fatalf("startup command = %v, want %v", m.Startup[0].Command, want)
	}
}

func TestManifest_ExactlyOneInteractivePickerPane(t *testing.T) {
	m := loadManifest(t)

	if len(m.Panes) != 1 {
		t.Fatalf("panes = %d, want exactly 1", len(m.Panes))
	}
	p := m.Panes[0]
	if p.ID != "picker" {
		t.Fatalf("pane id = %q, want picker", p.ID)
	}
	if p.Title != "Shep" {
		t.Fatalf("pane title = %q, want Shep", p.Title)
	}
	if p.Placement != "popup" {
		t.Fatalf("pane placement = %q, want popup", p.Placement)
	}
	if p.Width != "90%" {
		t.Fatalf("pane width = %q, want 90%%", p.Width)
	}
	if p.Height != "80%" {
		t.Fatalf("pane height = %q, want 80%%", p.Height)
	}
	wantCmd := []string{"bash", "scripts/run-shep.sh", "open"}
	if !equalSlice(p.Command, wantCmd) {
		t.Fatalf("pane command = %v, want %v", p.Command, wantCmd)
	}
}

func TestManifest_DeclaresExpectedActions(t *testing.T) {
	m := loadManifest(t)

	expectedActions := map[string]action{
		"open": {
			ID:          "open",
			Title:       "Open Shep picker",
			Description: "Open the interactive project and workspace picker popup",
			Contexts:    []string{"global", "workspace", "tab", "pane"},
			Command:     []string{"bash", "scripts/open-picker.sh"},
		},
		"jump-back": {
			ID:          "jump-back",
			Title:       "Jump to previous workspace",
			Description: "Toggle between the two most recently focused workspaces",
			Contexts:    []string{"workspace"},
			Command:     []string{"bash", "scripts/run-shep.sh", "jump-back"},
		},
		"start-history": {
			ID:          "start-history",
			Title:       "Start Shep history collector",
			Description: "Operator recovery for the focus-history collector",
			Contexts:    []string{"workspace"},
			Command:     []string{"bash", "scripts/run-shep.sh", "watch-history"},
		},
		"doctor": {
			ID:          "doctor",
			Title:       "Shep doctor",
			Description: "Check configured workspaces and published PATH name",
			Contexts:    []string{"global"},
			Command:     []string{"bash", "scripts/run-shep.sh", "doctor"},
		},
	}

	if len(m.Actions) != len(expectedActions) {
		t.Fatalf("actions count = %d, want %d", len(m.Actions), len(expectedActions))
	}

	for _, act := range m.Actions {
		exp, ok := expectedActions[act.ID]
		if !ok {
			t.Fatalf("unexpected action id %q", act.ID)
		}
		if act.Title != exp.Title {
			t.Errorf("action %q title = %q, want %q", act.ID, act.Title, exp.Title)
		}
		if act.Description != exp.Description {
			t.Errorf("action %q description = %q, want %q", act.ID, act.Description, exp.Description)
		}
		if !equalSlice(act.Contexts, exp.Contexts) {
			t.Errorf("action %q contexts = %v, want %v", act.ID, act.Contexts, exp.Contexts)
		}
		if !equalSlice(act.Command, exp.Command) {
			t.Errorf("action %q command = %v, want %v", act.ID, act.Command, exp.Command)
		}
	}
}

func TestManifest_DeclaresZeroEventHooks(t *testing.T) {
	m := loadManifest(t)

	if len(m.Events) != 0 {
		t.Fatalf("event hooks = %d, want 0; per-event hooks must not be registered", len(m.Events))
	}
}

func TestManifest_NeverLaunchesAnUnverifiedBinaryFromPath(t *testing.T) {
	m := loadManifest(t)

	var commands [][]string
	for _, s := range m.Startup {
		commands = append(commands, s.Command)
	}
	for _, p := range m.Panes {
		commands = append(commands, p.Command)
	}
	for _, a := range m.Actions {
		commands = append(commands, a.Command)
	}

	if len(commands) != 6 {
		t.Fatalf("expected exactly 6 declared commands (1 startup + 1 pane + 4 actions), got %d", len(commands))
	}

	for _, cmd := range commands {
		if len(cmd) == 0 {
			t.Fatal("a declared command is empty")
		}
		first := cmd[0]
		if first == "shep" {
			t.Fatalf("command[0] = %q, bare PATH lookup forbidden", first)
		}
		if first != "bash" {
			t.Fatalf("command[0] = %q, want bash", first)
		}
		if len(cmd) < 2 || !strings.HasPrefix(cmd[1], "scripts/") {
			t.Fatalf("bash command %v must run a script under scripts/", cmd)
		}
		if cmd[1] != "scripts/run-shep.sh" && cmd[1] != "scripts/open-picker.sh" {
			t.Fatalf("command %v uses an unapproved plugin script", cmd)
		}
	}
}

func TestRunShepScript_IsCommittedAndExecutable(t *testing.T) {
	info, err := os.Stat("scripts/run-shep.sh")
	if err != nil {
		t.Fatalf("stat run-shep.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("run-shep.sh mode = %v, want executable", info.Mode())
	}
	tracked, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contrib/herdr-plugin/scripts/run-shep.sh"))
	if err != nil || len(tracked) == 0 {
		t.Fatalf("run-shep.sh is not present in repository: %v", err)
	}
}

func TestRunShepScript_PreparesPathWithoutSourcingProfiles(t *testing.T) {
	pluginRoot := t.TempDir()
	scriptDir := filepath.Join(pluginRoot, "scripts")
	binDir := filepath.Join(pluginRoot, "bin")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(pluginRoot, "record.json")
	fakeShep := filepath.Join(binDir, "shep")
	fake := "#!/bin/bash\nset -euo pipefail\nprintf '%s' \"$PATH\" > \"$RECORD\"\nprintf '\\n%s' \"$@\" >> \"$RECORD\"\n"
	if err := os.WriteFile(fakeShep, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyPath(filepath.Join(repositoryRoot(t), "contrib/herdr-plugin/scripts/run-shep.sh"), filepath.Join(scriptDir, "run-shep.sh")); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(pluginRoot, "home")
	for _, dir := range []string{filepath.Join(home, ".local/bin"), filepath.Join(home, ".cargo/bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	profileMarker := filepath.Join(home, "profile-marker")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("touch "+profileMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(pluginRoot, "old-bin")
	cmd := exec.Command("bash", filepath.Join(scriptDir, "run-shep.sh"), "list", "--format", "json")
	cmd.Env = []string{"HOME=" + home, "PATH=" + oldPath, "RECORD=" + dataPath, "USER=test-user", "SHELL=/bin/zsh"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run wrapper: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(contents), "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("recorded output = %q", contents)
	}
	pathEntries := strings.Split(parts[0], ":")
	wantPrefix := []string{filepath.Join(home, ".local/bin"), filepath.Join(home, ".cargo/bin")}
	if !equalSlice(pathEntries[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("PATH prefix = %v, want %v", pathEntries, wantPrefix)
	}
	if strings.Count(parts[0], filepath.Join(home, ".cargo/bin")) != 1 || !strings.HasSuffix(parts[0], oldPath) {
		t.Fatalf("PATH = %q, want deduplicated prepended dirs plus original PATH", parts[0])
	}
	if parts[1] != "list\n--format\njson" {
		t.Fatalf("argv = %q", parts[1])
	}
	if _, err := os.Stat(profileMarker); !os.IsNotExist(err) {
		t.Fatalf("shell profile was sourced: marker stat error = %v", err)
	}
}

func TestManifest_DoesNotCommitAPluginBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err == nil {
		out, err := exec.Command("git", "ls-files", "--error-unmatch", "bin/shep").CombinedOutput()
		if err == nil {
			t.Fatalf("bin/shep must not be committed to git: %s", out)
		}
		return
	}
	if _, err := os.Stat("bin/shep"); err == nil {
		t.Fatal("bin/shep must not be committed; it is an install-time artifact")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stating bin/shep: %v", err)
	}
}

func equalSlice(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
