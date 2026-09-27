// Package herdrplugin holds the Herdr plugin manifest for Shep.
// The tests here verify the manifest contract against Herdr requirements.
package herdrplugin

import (
	"os"
	"os/exec"
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
	want := []string{"go", "build", "-o", "bin/shep", "./cmd/shep"}
	if !equalSlice(m.Build[0].Command, want) {
		t.Fatalf("build command = %v, want %v", m.Build[0].Command, want)
	}
}

func TestManifest_ExactlyOneStartupHookRunningCollector(t *testing.T) {
	m := loadManifest(t)

	if len(m.Startup) != 1 {
		t.Fatalf("startup hooks = %d, want exactly 1", len(m.Startup))
	}
	want := []string{"./bin/shep", "watch-history"}
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
	wantCmd := []string{"./bin/shep", "open"}
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
			Command:     []string{"./bin/shep", "jump-back"},
		},
		"start-history": {
			ID:          "start-history",
			Title:       "Start Shep history collector",
			Description: "Operator recovery for the focus-history collector",
			Contexts:    []string{"workspace"},
			Command:     []string{"./bin/shep", "watch-history"},
		},
		"doctor": {
			ID:          "doctor",
			Title:       "Shep doctor",
			Description: "Check configured workspaces and published PATH name",
			Contexts:    []string{"global"},
			Command:     []string{"./bin/shep", "doctor"},
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
		if first != "./bin/shep" && first != "bash" {
			t.Fatalf("command[0] = %q, want ./bin/shep or bash", first)
		}
		if first == "bash" {
			if len(cmd) < 2 || !strings.HasPrefix(cmd[1], "scripts/") {
				t.Fatalf("bash command %v must run a script under scripts/", cmd)
			}
		}
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
