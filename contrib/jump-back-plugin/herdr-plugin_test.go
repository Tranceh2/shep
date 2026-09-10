// Package jumpbackplugin holds the Herdr plugin manifest that runs the shep
// focus-history collector. The tests here are the manifest's contract: they
// parse the real shipped file and assert the shape R9 requires.
package jumpbackplugin

import (
	"os"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// manifest mirrors the Herdr v0.8.2 plugin manifest fields this plugin uses.
// The events table is declared ONLY so a parse can prove it is absent: this
// plugin must register zero focus/close event hooks.
type manifest struct {
	ID              string     `toml:"id"`
	Name            string     `toml:"name"`
	Version         string     `toml:"version"`
	MinHerdrVersion string     `toml:"min_herdr_version"`
	Description     string     `toml:"description"`
	Platforms       []string   `toml:"platforms"`
	Build           []buildCmd `toml:"build"`
	Startup         []startup  `toml:"startup"`
	Actions         []action   `toml:"actions"`
	Events          []event    `toml:"events"`
	Panes           []pane     `toml:"panes"`
}

type buildCmd struct {
	Command []string `toml:"command"`
}

type startup struct {
	Command   []string `toml:"command"`
	Platforms []string `toml:"platforms"`
}

type action struct {
	ID       string   `toml:"id"`
	Title    string   `toml:"title"`
	Contexts []string `toml:"contexts"`
	Command  []string `toml:"command"`
}

type event struct {
	On      string   `toml:"on"`
	Command []string `toml:"command"`
}

type pane struct {
	ID      string   `toml:"id"`
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

// TestManifest_ParsesWithRequiredHerdrMetadata proves the file is valid TOML
// and carries the top-level fields Herdr v0.8.2 requires to link a plugin.
func TestManifest_ParsesWithRequiredHerdrMetadata(t *testing.T) {
	m := loadManifest(t)

	if m.ID != "tranceh2.shep-jump-back" {
		t.Fatalf("id = %q, want tranceh2.shep-jump-back (must not collide with the existing tranceh2.shep registration)", m.ID)
	}
	if m.Name == "" {
		t.Fatal("name is required by Herdr")
	}
	if m.Version == "" {
		t.Fatal("version is required by Herdr")
	}
	if m.MinHerdrVersion != "0.8.2" {
		t.Fatalf("min_herdr_version = %q, want 0.8.2 (the pinned wire/manifest contract)", m.MinHerdrVersion)
	}
	if len(m.Platforms) == 0 {
		t.Fatal("platforms must be declared so linking does not warn")
	}
	for _, p := range m.Platforms {
		if p == "windows" {
			t.Fatal("windows must not be declared: the collector uses AF_UNIX sockets and flock")
		}
	}
}

// TestManifest_ExactlyOneStartupHookRunningTheCollector pins R9.S1's first
// half: one startup hook, invoking the plugin-local verified binary.
func TestManifest_ExactlyOneStartupHookRunningTheCollector(t *testing.T) {
	m := loadManifest(t)

	if len(m.Startup) != 1 {
		t.Fatalf("startup hooks = %d, want exactly 1", len(m.Startup))
	}
	want := []string{"./bin/shep", "watch-history"}
	if !equalArgv(m.Startup[0].Command, want) {
		t.Fatalf("startup command = %v, want %v", m.Startup[0].Command, want)
	}
}

// TestManifest_ExactlyOneExplicitStartRecoverAction pins R9.S1's second half:
// one explicit action, running the same argv as the startup hook so an
// operator recovery and an autostart are the same operation.
func TestManifest_ExactlyOneExplicitStartRecoverAction(t *testing.T) {
	m := loadManifest(t)

	if len(m.Actions) != 1 {
		t.Fatalf("actions = %d, want exactly 1 explicit start/recover action", len(m.Actions))
	}
	a := m.Actions[0]
	if a.ID == "" || a.Title == "" {
		t.Fatalf("action needs a non-empty id and title, got id=%q title=%q", a.ID, a.Title)
	}
	if len(a.Contexts) == 0 {
		t.Fatal("action must declare contexts so Herdr can offer it")
	}
	want := []string{"./bin/shep", "watch-history"}
	if !equalArgv(a.Command, want) {
		t.Fatalf("action command = %v, want %v (same argv as the startup hook)", a.Command, want)
	}
}

// TestManifest_DeclaresZeroEventHooksAndNoOverlayPane is the negative control
// for R9: the design forbids focus/close event hooks (they would spawn a
// process per event, which the collector exists to avoid) and forbids
// restoring the old overlay wrapper.
func TestManifest_DeclaresZeroEventHooksAndNoOverlayPane(t *testing.T) {
	m := loadManifest(t)

	if len(m.Events) != 0 {
		t.Fatalf("event hooks = %d, want 0; a per-event hook would spawn a process per focus change", len(m.Events))
	}
	if len(m.Panes) != 0 {
		t.Fatalf("panes = %d, want 0; this plugin must not restore the overlay picker wrapper", len(m.Panes))
	}
	if len(m.Build) != 0 {
		t.Fatalf("build steps = %d, want 0; the operator copies a verified binary rather than letting install build one", len(m.Build))
	}
}

// TestManifest_NeverLaunchesAnUnverifiedBinaryFromPath proves every declared
// command is the plugin-local relative path. A bare "shep" would resolve
// through the server's PATH and could launch an unverified binary.
func TestManifest_NeverLaunchesAnUnverifiedBinaryFromPath(t *testing.T) {
	m := loadManifest(t)

	var commands [][]string
	for _, s := range m.Startup {
		commands = append(commands, s.Command)
	}
	for _, a := range m.Actions {
		commands = append(commands, a.Command)
	}
	if len(commands) != 2 {
		t.Fatalf("expected exactly 2 declared commands (1 startup + 1 action), got %d", len(commands))
	}

	for _, cmd := range commands {
		if len(cmd) == 0 {
			t.Fatal("a declared command is empty")
		}
		if cmd[0] != "./bin/shep" {
			t.Fatalf("command[0] = %q, want the plugin-local ./bin/shep (never a bare PATH lookup)", cmd[0])
		}
	}
}

// TestManifest_DoesNotCommitAPluginBinary proves the manifest ships without a
// committed executable; bin/shep is an installation artifact the operator
// copies in after verifying it.
func TestManifest_DoesNotCommitAPluginBinary(t *testing.T) {
	if _, err := os.Stat("bin/shep"); err == nil {
		t.Fatal("bin/shep must not be committed; it is an install-time artifact")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stating bin/shep: %v", err)
	}
}

func equalArgv(got, want []string) bool {
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
