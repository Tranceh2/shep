// Package herdrplugin holds the Herdr plugin manifest for Shep.
// The tests here verify the manifest contract against Herdr requirements.
package herdrplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	if m.Version != "1.0.0" {
		t.Fatalf("version = %q, want 1.0.0", m.Version)
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
	want := []string{"./scripts/build.sh"}
	if !equalSlice(m.Build[0].Command, want) {
		t.Fatalf("build command = %v, want %v", m.Build[0].Command, want)
	}
}

// TestManifest_BuildsFromHerdrPluginWorkingDirectory proves the build step,
// building from source (SHEP_PLUGIN_BUILD=source), compiles the checkout into
// the plugin's bin/ from the plugin directory Herdr runs it in.
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
	cmd.Env = append(os.Environ(), "GOPROXY=off", "SHEP_PLUGIN_BUILD=source")
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

// gitAvailable reports whether THIS source tree can answer questions about
// tracked files. `git rev-parse` searches upwards, so an enclosing repository
// would otherwise answer for an exported copy nested inside one and reject its
// files as untracked. The discovered worktree must be the tree under test.
func gitAvailable(sourceRoot string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	out, err := exec.Command("git", "-C", sourceRoot, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	discovered, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	expected, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return false
	}
	return discovered == expected
}

func TestGitAvailable_AcceptsOnlyTheTreeUnderTest(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	// gitAvailable inherits this process's environment, so any of the variables
	// below would aim discovery at the runner's own repository and decide these
	// cases for reasons unrelated to the function. t.Setenv cannot scrub them in
	// a parallel test, so say so instead of reporting a meaningless pass.
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		if _, set := os.LookupEnv(name); set {
			t.Skipf("%s is set, so git discovery is redirected away from the fixtures", name)
		}
	}
	worktree := t.TempDir()
	initRepo := exec.Command("git", "-C", worktree, "init")
	if out, err := initRepo.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	for _, tc := range []struct {
		name string
		root string
		want bool
	}{
		{"the worktree root itself", worktree, true},
		{"a directory inside the worktree but not its root", nested, false},
		{"a directory in no repository at all", outside, false},
	} {
		if got := gitAvailable(tc.root); got != tc.want {
			t.Errorf("gitAvailable(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// assertTracked proves relativePath is tracked in repoRoot. The pathspec is
// derived from the resolved root rather than a hardcoded package prefix, and a
// missing file is reported separately because ls-files cannot distinguish an
// unmatched pathspec from a genuine tracking regression.
func assertTracked(t *testing.T, repoRoot, relativePath string) {
	t.Helper()
	absolutePath, err := filepath.Abs(relativePath)
	if err != nil {
		t.Fatalf("resolve %s: %v", relativePath, err)
	}
	tracked, err := filepath.Rel(repoRoot, absolutePath)
	if err != nil {
		t.Fatalf("locate %s inside %s: %v", relativePath, repoRoot, err)
	}
	tracked = filepath.ToSlash(tracked)
	if _, err := os.Stat(filepath.Join(repoRoot, tracked)); err != nil {
		t.Fatalf("pathspec %s does not exist in %s: %v", tracked, repoRoot, err)
	}
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "--error-unmatch", tracked).CombinedOutput()
	if err != nil {
		// Keep git's own output: a tracking regression and a failing git read
		// the same way without it.
		t.Fatalf("%s is not tracked: %v: %s", tracked, err, out)
	}
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
	want := []string{"./scripts/run-shep.sh", "watch-history"}
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
	wantCmd := []string{"./scripts/run-shep.sh", "open"}
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
			Command:     []string{"./scripts/open-picker.sh"},
		},
		"jump-back": {
			ID:          "jump-back",
			Title:       "Jump to previous workspace",
			Description: "Toggle between the two most recently focused workspaces",
			Contexts:    []string{"workspace"},
			Command:     []string{"./scripts/run-shep.sh", "jump-back"},
		},
		"start-history": {
			ID:          "start-history",
			Title:       "Start Shep history collector",
			Description: "Operator recovery for the focus-history collector",
			Contexts:    []string{"workspace"},
			Command:     []string{"./scripts/run-shep.sh", "watch-history"},
		},
		"doctor": {
			ID:          "doctor",
			Title:       "Shep doctor",
			Description: "Check configured workspaces, the picker theme and the published PATH name",
			Contexts:    []string{"global"},
			Command:     []string{"./scripts/run-shep.sh", "doctor"},
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
		if first == "shep" || first == "bash" || first == "sh" {
			t.Fatalf("command[0] = %q, PATH-resolved launcher forbidden", first)
		}
		if !strings.HasPrefix(first, "./scripts/") {
			t.Fatalf("command %v must use a plugin-relative script path", cmd)
		}
		if first != "./scripts/build.sh" && first != "./scripts/run-shep.sh" && first != "./scripts/open-picker.sh" {
			t.Fatalf("command %v uses an unapproved plugin script", cmd)
		}
	}
}

func TestPluginScripts_AreTrackedExecutableAndDirectlyInvokable(t *testing.T) {
	m := loadManifest(t)
	commands := make([][]string, 0, len(m.Build)+len(m.Startup)+len(m.Panes)+len(m.Actions)+len(m.Events))
	for _, build := range m.Build {
		commands = append(commands, build.Command)
	}
	for _, startup := range m.Startup {
		commands = append(commands, startup.Command)
	}
	for _, pane := range m.Panes {
		commands = append(commands, pane.Command)
	}
	for _, action := range m.Actions {
		commands = append(commands, action.Command)
	}
	for _, event := range m.Events {
		commands = append(commands, event.Command)
	}

	seen := make(map[string]bool)
	for _, command := range commands {
		if len(command) == 0 || !strings.HasPrefix(command[0], "./scripts/") {
			continue
		}
		relativePath := strings.TrimPrefix(command[0], "./")
		if seen[relativePath] {
			continue
		}
		seen[relativePath] = true

		info, err := os.Stat(relativePath)
		if err != nil {
			t.Fatalf("stat %s: %v", relativePath, err)
		}
		// Herdr execs these paths directly, so the owner-executable bit is the
		// requirement. An exact 0755 would fail under a different umask in a
		// source archive or module-cache extraction without telling us anything
		// more about whether Herdr can start the script.
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("%s mode = %o, want an owner-executable bit", relativePath, info.Mode().Perm())
		}
		// Tracking is a repository property, so it is only assertable where this
		// tree's own repository metadata exists. A vendored copy, an exported
		// tarball, or a builder without git still proves everything else here,
		// so the skip is logged rather than silent.
		repoRoot := repositoryRoot(t)
		if gitAvailable(repoRoot) {
			assertTracked(t, repoRoot, relativePath)
		} else {
			t.Logf("skipping the tracking assertion for %s: no repository metadata for %s", relativePath, repoRoot)
		}
		data, err := os.ReadFile(relativePath)
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		if !strings.HasPrefix(string(data), "#!") {
			t.Fatalf("%s does not begin with a shebang", relativePath)
		}
		// Runtime scripts must remain executable with only POSIX sh; build.sh is
		// intentionally different because install-time builds require Bash and Go.
		if relativePath == "scripts/run-shep.sh" || relativePath == "scripts/open-picker.sh" {
			if !strings.HasPrefix(string(data), "#!/bin/sh\n") {
				t.Fatalf("%s does not declare #!/bin/sh", relativePath)
			}
		}
	}
}

func writeFakePluginShep(t *testing.T, path string) {
	t.Helper()
	const script = `#!/bin/sh
set -eu
: "${RECORD:?RECORD must be set}"
case $RECORD in
  /*) ;;
  *) echo "RECORD must be absolute" >&2; exit 2 ;;
esac
printf '%s\n' "${PATH-}" >"$RECORD"
first=1
for argument do
  if [ "$first" -eq 1 ]; then
    printf '%s' "$argument" >>"$RECORD"
    first=0
  else
    printf '\n%s' "$argument" >>"$RECORD"
  fi
done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake plugin shep: %v", err)
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
	writeFakePluginShep(t, fakeShep)
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
	cmd := exec.Command("/bin/sh", filepath.Join(scriptDir, "run-shep.sh"), "list", "--format", "json")
	cmd.Env = []string{"HOME=" + home, "PATH=" + oldPath, "RECORD=" + dataPath, "USER=test-user", "SHELL=fixture-shell"}
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
	if len(pathEntries) < len(wantPrefix) {
		t.Fatalf("PATH entries = %v, want prefix %v", pathEntries, wantPrefix)
	}
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

// TestRunShepScript_DeduplicatesPathKeepingFirstOccurrence pins the PATH the
// wrapper hands shep: every directory once, in first-occurrence order, with
// empty entries dropped and glob characters taken literally.
func TestRunShepScript_DeduplicatesPathKeepingFirstOccurrence(t *testing.T) {
	pluginRoot := t.TempDir()
	scriptDir := filepath.Join(pluginRoot, "scripts")
	binDir := filepath.Join(pluginRoot, "bin")
	for _, dir := range []string{scriptDir, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := filepath.Join(pluginRoot, "record")
	writeFakePluginShep(t, filepath.Join(binDir, "shep"))
	if err := copyPath(filepath.Join(repositoryRoot(t), "contrib/herdr-plugin/scripts/run-shep.sh"), filepath.Join(scriptDir, "run-shep.sh")); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(pluginRoot, "home")
	cargo := filepath.Join(home, ".cargo/bin")
	if err := os.MkdirAll(cargo, 0o755); err != nil {
		t.Fatal(err)
	}
	glob := filepath.Join(pluginRoot, "tools*")
	spaced := filepath.Join(pluginRoot, "my tools")
	original := []string{"/b", "", cargo, glob, "/a", "/b", spaced, "", "/a", glob, spaced}
	cmd := exec.Command("/bin/sh", filepath.Join(scriptDir, "run-shep.sh"), "probe")
	cmd.Env = []string{"HOME=" + home, "PATH=" + strings.Join(original, ":"), "RECORD=" + dataPath}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run wrapper: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.SplitN(string(contents), "\n", 2)[0], ":")
	seen := make(map[string]bool, len(got))
	for _, entry := range got {
		if entry == "" {
			t.Fatalf("PATH %q keeps an empty entry", got)
		}
		if seen[entry] {
			t.Fatalf("PATH %q repeats %q", got, entry)
		}
		seen[entry] = true
	}
	if got[0] != cargo {
		t.Fatalf("PATH %q, want the existing prepended %q first", got, cargo)
	}
	// The caller's entries follow in their first-occurrence order.
	var tail []string
	for _, entry := range got {
		if entry == "/b" || entry == "/a" || entry == glob || entry == spaced {
			tail = append(tail, entry)
		}
	}
	if want := []string{"/b", glob, "/a", spaced}; !equalSlice(tail, want) {
		t.Fatalf("caller entries in PATH = %q, want %q", tail, want)
	}
}

func TestRunShepScript_StartsWithoutBashOnPATH(t *testing.T) {
	pluginRoot := t.TempDir()
	scriptDir := filepath.Join(pluginRoot, "scripts")
	binDir := filepath.Join(pluginRoot, "bin")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(pluginRoot, "record")
	fakeShep := filepath.Join(binDir, "shep")
	writeFakePluginShep(t, fakeShep)
	if err := copyPath(filepath.Join(repositoryRoot(t), "contrib/herdr-plugin/scripts/run-shep.sh"), filepath.Join(scriptDir, "run-shep.sh")); err != nil {
		t.Fatal(err)
	}
	shOnlyPath := filepath.Join(pluginRoot, "sh-only")
	if err := os.MkdirAll(shOnlyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/sh", filepath.Join(shOnlyPath, "sh")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", filepath.Join(scriptDir, "run-shep.sh"), "probe")
	cmd.Env = []string{"PATH=" + shOnlyPath, "RECORD=" + dataPath}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run wrapper with bash-free PATH: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "bash") {
		t.Fatalf("wrapper execution unexpectedly required bash: %q", contents)
	}
	if !strings.HasSuffix(string(contents), "\nprobe") {
		t.Fatalf("fake shep did not receive argv under bash-free PATH: %q", contents)
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

// openPickerRun runs a copy of open-picker.sh from a temporary plugin root
// whose bin/shep and herdr binaries are fakes recording their argv, and
// returns what each one recorded ("" when it did not run).
func openPickerRun(t *testing.T, env ...string) (shep, herdr string) {
	t.Helper()
	pluginRoot := t.TempDir()
	scriptDir := filepath.Join(pluginRoot, "scripts")
	binDir := filepath.Join(pluginRoot, "bin")
	for _, dir := range []string{scriptDir, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyPath(filepath.Join(repositoryRoot(t), "contrib/herdr-plugin/scripts/open-picker.sh"), filepath.Join(scriptDir, "open-picker.sh")); err != nil {
		t.Fatal(err)
	}
	shepRecord, herdrRecord := filepath.Join(pluginRoot, "shep.rec"), filepath.Join(pluginRoot, "herdr.rec")
	fake := func(path, record string) {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + record + "\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake(filepath.Join(binDir, "shep"), shepRecord)
	herdrBin := filepath.Join(pluginRoot, "herdr")
	fake(herdrBin, herdrRecord)
	cmd := exec.Command("/bin/sh", filepath.Join(scriptDir, "open-picker.sh"))
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HERDR_BIN_PATH=" + herdrBin}, env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open-picker.sh: %v\n%s", err, output)
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	return read(shepRecord), read(herdrRecord)
}

// TestOpenPickerScript_InsideHerdrAsksShep proves the open action hands the
// popup to the plugin's shep when Herdr provides its socket, so the shortcut
// does not wait for the herdr CLI to start.
func TestOpenPickerScript_InsideHerdrAsksShep(t *testing.T) {
	shep, herdr := openPickerRun(t, "HERDR_SOCKET_PATH=/run/herdr.sock")
	if want := "popup\n--plugin\ntranceh2.shep\n--entrypoint\npicker"; shep != want {
		t.Errorf("shep argv = %q, want %q", shep, want)
	}
	if herdr != "" {
		t.Errorf("herdr CLI ran with %q, want it untouched", herdr)
	}
}

// TestOpenPickerScript_WithoutASocketUsesTheHerdrCLI proves the CLI still
// opens the popup when no socket is known.
func TestOpenPickerScript_WithoutASocketUsesTheHerdrCLI(t *testing.T) {
	shep, herdr := openPickerRun(t)
	if shep != "" {
		t.Errorf("shep ran with %q, want the herdr CLI", shep)
	}
	if want := "plugin\npane\nopen\n--plugin\ntranceh2.shep\n--entrypoint\npicker\n--placement\npopup"; herdr != want {
		t.Errorf("herdr argv = %q, want %q", herdr, want)
	}
}

// buildScriptRun runs a copy of scripts/build.sh from a temporary plugin root
// (manifest and scripts only, no checkout to build) with PATH set to path, and
// releases served from releases (file:// URLs). It returns the combined
// output, the plugin root and the run error.
func buildScriptRun(t *testing.T, path, releases string) (out, pluginRoot string, err error) {
	t.Helper()
	pluginRoot = filepath.Join(t.TempDir(), "contrib", "herdr-plugin")
	if err := os.MkdirAll(filepath.Join(pluginRoot, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := repositoryRoot(t)
	for _, rel := range []string{"herdr-plugin.toml", "scripts/build.sh"} {
		if err := copyPath(filepath.Join(repo, "contrib/herdr-plugin", rel), filepath.Join(pluginRoot, rel)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(pluginRoot, "scripts", "build.sh"))
	cmd.Dir = pluginRoot
	cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir(), "SHEP_RELEASE_URL=file://" + releases}
	data, err := cmd.CombinedOutput()
	return string(data), pluginRoot, err
}

// fakeRelease writes the release of the manifest's version for this
// platform under a new directory: an archive holding a stand-in shep, and a
// checksums.txt that matches it unless corrupt.
func fakeRelease(t *testing.T, version string, corrupt bool) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "v"+version)
	staging := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "shep"), []byte("#!/bin/sh\necho release-binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := fmt.Sprintf("shep_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	if out, err := exec.Command("tar", "-czf", filepath.Join(dir, archive), "-C", staging, "shep").CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(dir, archive))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if corrupt {
		digest = strings.Repeat("0", len(digest))
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(digest+"  "+archive+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}

// fakeGo writes a stand-in go that records its arguments and writes the -o
// output, so a source build can be observed without compiling.
func fakeGo(t *testing.T) (dir, record string) {
	t.Helper()
	dir = t.TempDir()
	record = filepath.Join(dir, "go.args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + record + "\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = -o ]; then printf '#!/bin/sh\\necho source-binary\\n' >\"$2\"; chmod +x \"$2\"; fi\n  shift\ndone\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, record
}

// TestBuildScript_InstallsTheVerifiedReleaseWithoutGo proves the plugin build
// needs no Go toolchain: it downloads the release archive of the manifest's
// version for this platform, checks it against checksums.txt and installs
// its binary.
func TestBuildScript_InstallsTheVerifiedReleaseWithoutGo(t *testing.T) {
	m := loadManifest(t)
	out, pluginRoot, err := buildScriptRun(t, "/usr/bin:/bin", fakeRelease(t, m.Version, false))
	if err != nil {
		t.Fatalf("build.sh: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(runBinary(t, filepath.Join(pluginRoot, "bin", "shep"))); got != "release-binary" {
		t.Errorf("bin/shep printed %q, want the release binary\n%s", got, out)
	}
}

// TestBuildScript_RefusesABadChecksumAndBuildsFromSource proves an archive
// that does not match the release checksum is never installed: the build
// says so and compiles the checkout instead.
func TestBuildScript_RefusesABadChecksumAndBuildsFromSource(t *testing.T) {
	m := loadManifest(t)
	goDir, record := fakeGo(t)
	out, pluginRoot, err := buildScriptRun(t, goDir+":/usr/bin:/bin", fakeRelease(t, m.Version, true))
	if err != nil {
		t.Fatalf("build.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "does not match the release checksum") {
		t.Errorf("output %q, want the checksum refusal", out)
	}
	if got := strings.TrimSpace(runBinary(t, filepath.Join(pluginRoot, "bin", "shep"))); got != "source-binary" {
		t.Errorf("bin/shep printed %q, want the source build", got)
	}
	if args, _ := os.ReadFile(record); !strings.Contains(string(args), "main.version="+m.Version) {
		t.Errorf("go build args %q, want the manifest version for a tagless checkout", args)
	}
}

// TestBuildScript_WithoutReleaseOrGoFailsClearly proves the build fails with
// a message naming what is missing when there is neither a release to
// download nor a Go toolchain to build with.
func TestBuildScript_WithoutReleaseOrGoFailsClearly(t *testing.T) {
	out, _, err := buildScriptRun(t, "/usr/bin:/bin", t.TempDir())
	if err == nil {
		t.Fatalf("build.sh succeeded without a release or Go:\n%s", out)
	}
	if !strings.Contains(out, "needs Go 1.26.4+") {
		t.Errorf("output %q, want it to say Go is needed", out)
	}
}
