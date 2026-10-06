package herdr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

type fakeRunner struct {
	script []fakeCall
	calls  []string
	argv   [][]string
}

type fakeCall struct {
	match string
	out   []byte
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	f.argv = append(f.argv, append([]string{name}, args...))
	for i, call := range f.script {
		if call.match == key {
			f.script = append(f.script[:i], f.script[i+1:]...)
			return call.out, call.err
		}
	}
	return nil, errors.New("not scripted: " + key)
}

func TestDriverRenamePane_UsesArgvSafeLabels(t *testing.T) {
	for _, tt := range []struct {
		name  string
		label *string
		want  []string
	}{
		{name: "plain label", label: stringPtr("api"), want: []string{"herdr", "pane", "rename", "w1:p1", "api"}},
		{name: "spaces and shell metacharacters stay one argument", label: stringPtr("my logs; rm -rf / $`\\|&"), want: []string{"herdr", "pane", "rename", "w1:p1", "my logs; rm -rf / $`\\|&"}},
		{name: "clear", label: stringPtr(""), want: []string{"herdr", "pane", "rename", "w1:p1", "--clear"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{script: []fakeCall{{match: strings.Join(tt.want, " ")}}}
			if err := New("herdr", WithRunner(runner)).RenamePane(context.Background(), "w1:p1", tt.label); err != nil {
				t.Fatalf("RenamePane: %v", err)
			}
			if len(runner.argv) != 1 || !reflect.DeepEqual(runner.argv[0], tt.want) {
				t.Fatalf("argv = %v, want %v", runner.argv, tt.want)
			}
		})
	}
}

func TestDriverRenamePane_RejectsInvalidRequestsWithoutExec(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pane  string
		label *string
		want  string
	}{
		{name: "empty pane id", want: "empty pane id", pane: "", label: stringPtr("api")},
		{name: "nil label", want: "nil label", pane: "w1:p1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			err := New("herdr", WithRunner(runner)).RenamePane(context.Background(), tt.pane, tt.label)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RenamePane error = %v, want %q", err, tt.want)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("calls = %v, want no exec", runner.calls)
			}
		})
	}
}

func TestDriverRenamePane_RejectsFlagLikeLabelsWithoutExec(t *testing.T) {
	for _, tt := range []struct {
		name  string
		label string
	}{
		{name: "clear flag", label: "--clear"},
		{name: "short flag", label: "-x"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			err := New("herdr", WithRunner(runner)).RenamePane(context.Background(), "w1:p1", &tt.label)
			if err == nil || !strings.Contains(err.Error(), "w1:p1") || !strings.Contains(err.Error(), tt.label) || !strings.Contains(err.Error(), "positional label") {
				t.Fatalf("RenamePane error = %v, want pane, label, and positional-label context", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("calls = %v, want no exec", runner.calls)
			}
		})
	}
}

func TestDriverRenamePane_WrapsFailureWithPaneAndLabel(t *testing.T) {
	label := "my logs"
	runner := &fakeRunner{script: []fakeCall{{match: "herdr pane rename w1:p1 my logs", err: errors.New("exit status 1")}}}
	err := New("herdr", WithRunner(runner)).RenamePane(context.Background(), "w1:p1", &label)
	if err == nil || !strings.Contains(err.Error(), "w1:p1") || !strings.Contains(err.Error(), label) {
		t.Fatalf("RenamePane error = %v, want pane and label context", err)
	}
}

func TestDriverClose_ExposesHerdrStderr(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{{match: "herdr workspace close w1", err: &exec.ExitError{Stderr: []byte("workspace_group_close_required")}}}}
	err := New("herdr", WithRunner(runner)).CloseWorkspace(context.Background(), "w1")
	if err == nil || !strings.Contains(err.Error(), "workspace_group_close_required") {
		t.Fatalf("workspace close error = %v", err)
	}
}

func TestDriverClose_ArgvGuardsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		close func(*Driver, string) error
	}{
		{"pane", func(d *Driver, id string) error { return d.ClosePane(context.Background(), id) }},
		{"tab", func(d *Driver, id string) error { return d.CloseTab(context.Background(), id) }},
		{"workspace", func(d *Driver, id string) error { return d.CloseWorkspace(context.Background(), id) }},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			runner := &fakeRunner{script: []fakeCall{{match: "herdr " + tc.kind + " close id;echo unsafe"}, {match: "herdr " + tc.kind + " close id", err: errors.New("workspace_group_close_required")}}}
			d := New("herdr", WithRunner(runner))
			if err := tc.close(d, ""); err == nil || !strings.Contains(err.Error(), "empty "+tc.kind+" id") || len(runner.argv) != 0 {
				t.Fatalf("empty id: err=%v argv=%v", err, runner.argv)
			}
			if err := tc.close(d, "id;echo unsafe"); err != nil {
				t.Fatal(err)
			}
			if want := []string{"herdr", tc.kind, "close", "id;echo unsafe"}; !reflect.DeepEqual(runner.argv[0], want) {
				t.Fatalf("argv=%v, want %v", runner.argv[0], want)
			}
			if err := tc.close(d, "id"); err == nil || !strings.Contains(err.Error(), "workspace_group_close_required") || !strings.Contains(err.Error(), tc.kind+" close id") {
				t.Fatalf("close failure=%v", err)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }

func TestDriverSnapshot_UsesInjectedAuthoritativeBinary(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "herdr")
	if err := os.WriteFile(binary, []byte("fake"), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	runner := &fakeRunner{script: []fakeCall{{match: binary + " api snapshot", out: []byte(`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}}}
	driver := New("herdr", WithRunner(runner), WithBinaryEnv(func(string) (string, bool) {
		return binary, true
	}))
	if _, err := driver.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := runner.calls[0]; got != binary+" api snapshot" {
		t.Fatalf("runner received %q, want authoritative binary %q", got, binary)
	}
}

func TestDriverSnapshot_BinaryEnvironmentFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  string
		want string
	}{
		{name: "unset", want: "herdr"},
		{name: "blank", env: "   ", want: "herdr"},
		{name: "invalid path", env: filepath.Join(t.TempDir(), "missing-herdr"), want: "herdr"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			binaryEnv := func(string) (string, bool) { return tt.env, tt.env != "" }
			runner := &fakeRunner{script: []fakeCall{{match: tt.want + " api snapshot", out: []byte(`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}}}
			driver := New("herdr", WithRunner(runner), WithBinaryEnv(binaryEnv))
			if _, err := driver.Snapshot(context.Background()); err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			if runner.calls[0] != tt.want+" api snapshot" {
				t.Fatalf("runner received %q, want %q", runner.calls[0], tt.want)
			}
		})
	}
}

func TestDriverSnapshot_BinaryEnvironmentRejectsNonRegularFile(t *testing.T) {
	dir := t.TempDir()
	nonRegular := filepath.Join(dir, "herdr-dir")
	if err := os.Mkdir(nonRegular, 0o700); err != nil {
		t.Fatalf("mkdir non-regular candidate: %v", err)
	}

	runner := &fakeRunner{script: []fakeCall{{match: "configured-herdr api snapshot", out: []byte(`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}}}
	driver := New("configured-herdr", WithRunner(runner), WithBinaryEnv(func(string) (string, bool) {
		return nonRegular, true
	}))
	if _, err := driver.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if runner.calls[0] != "configured-herdr api snapshot" {
		t.Fatalf("runner received %q, want configured fallback for non-regular path", runner.calls[0])
	}
}

func TestDriverSnapshot_BinaryEnvironmentAcceptsSymlinkToRegularExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "herdr-real")
	link := filepath.Join(dir, "herdr-link")
	if err := os.WriteFile(target, []byte("fake"), 0o700); err != nil {
		t.Fatalf("write executable target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink executable target: %v", err)
	}

	runner := &fakeRunner{script: []fakeCall{{match: link + " api snapshot", out: []byte(`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}}}
	driver := New("configured-herdr", WithRunner(runner), WithBinaryEnv(func(string) (string, bool) {
		return link, true
	}))
	if _, err := driver.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if runner.calls[0] != link+" api snapshot" {
		t.Fatalf("runner received %q, want symlink path %q", runner.calls[0], link)
	}
}

func TestDriverSnapshot_UsesConfiguredFallbackForValidRelativeBinary(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{{match: "configured-herdr api snapshot", out: []byte(`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}}}
	driver := New("configured-herdr", WithRunner(runner), WithBinaryEnv(func(string) (string, bool) {
		return "", false
	}))
	if _, err := driver.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if runner.calls[0] != "configured-herdr api snapshot" {
		t.Fatalf("runner received %q, want configured fallback", runner.calls[0])
	}
}

func TestDriverSnapshot_ParsesOnlyFullGeneration(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{{match: "herdr api snapshot", out: []byte(`{
"id":"cli:api:snapshot","result":{"snapshot":{
"focused_workspace_id":"w1","focused_tab_id":"w1:t1","focused_pane_id":"w1:p1",
"workspaces":[{"workspace_id":"w1","label":"project","active_tab_id":"w1:t1","focused":true},{"label":"discard"}],
"tabs":[{"tab_id":"w1:t1","workspace_id":"w1","label":null,"number":1,"pane_count":1},{"workspace_id":"w1"}],
"panes":[{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"/project","agent":"opencode","agent_status":"working","terminal_title":"fixing bug","unknown":true},{"workspace_id":"w1"}]
}}}`)}}}

	snapshot, err := New("herdr", WithRunner(runner)).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "herdr api snapshot" {
		t.Fatalf("calls = %v, want exactly [herdr api snapshot]", runner.calls)
	}
	if len(snapshot.Workspaces) != 1 || len(snapshot.Tabs) != 1 || len(snapshot.Panes) != 1 {
		t.Fatalf("snapshot did not drop incomplete records: %+v", snapshot)
	}
	if snapshot.Panes[0].AgentStatus != "working" || snapshot.Panes[0].Agent != "opencode" || snapshot.Panes[0].TerminalTitle != "fixing bug" || snapshot.FocusedPaneID != "w1:p1" {
		t.Errorf("snapshot = %+v, want focused working pane with agent metadata", snapshot)
	}
}

func TestDriverSnapshot_EmptyMalformedAndCommandError(t *testing.T) {
	for _, tt := range []struct {
		name    string
		call    fakeCall
		wantErr bool
	}{
		{name: "empty is valid", call: fakeCall{match: "herdr api snapshot", out: []byte(`{"id":"cli:api:snapshot","result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}},
		{name: "malformed errors", call: fakeCall{match: "herdr api snapshot", out: []byte(`{`)}, wantErr: true},
		{name: "command errors", call: fakeCall{match: "herdr api snapshot", err: errors.New("daemon down")}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New("herdr", WithRunner(&fakeRunner{script: []fakeCall{tt.call}})).Snapshot(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Snapshot error = %v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && (len(got.Workspaces) != 0 || len(got.Tabs) != 0 || len(got.Panes) != 0) {
				t.Errorf("empty snapshot = %+v", got)
			}
		})
	}
}

// TestDriverListSessions_ParsesVerifiedEnvelope verifies the sessions command
// accepts only its documented top-level envelope, ignores unknown fields, and
// retains named records even when optional metadata is absent.
func TestDriverListSessions_ParsesVerifiedEnvelope(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{{match: "herdr session list --json", out: []byte(`{
  "sessions": [
    {"name":"default","running":true,"default":true,"session_dir":"/tmp/default","socket_path":"/tmp/default.sock","unknown":"kept-out"},
    {"name":"stopped","running":false},
    {"running":true,"socket_path":"/tmp/nameless.sock"}
  ],
  "future_field": {"ignored": true}
}`)}}}

	sessions, err := New("herdr", WithRunner(runner)).ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "herdr session list --json" {
		t.Errorf("calls = %v, want [herdr session list --json]", runner.calls)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v, want two named records", sessions)
	}
	if got, want := sessions[0], (source.Session{Name: "default", Running: true, Default: true, SessionDir: "/tmp/default", SocketPath: "/tmp/default.sock"}); got != want {
		t.Errorf("first session = %+v, want %+v", got, want)
	}
	if got, want := sessions[1], (source.Session{Name: "stopped"}); got != want {
		t.Errorf("partial session = %+v, want %+v", got, want)
	}
}

// TestDriverListSessions_RejectsUnverifiedOrBrokenEnvelope ensures legacy
// result-wrapped payloads, malformed JSON, and CLI errors never become a
// usable session list.
func TestDriverListSessions_RejectsUnverifiedOrBrokenEnvelope(t *testing.T) {
	for _, tt := range []struct {
		name string
		call fakeCall
	}{
		{name: "legacy result wrapper", call: fakeCall{match: "herdr session list --json", out: []byte(`{"result":{"sessions":[{"name":"legacy"}]}}`)}},
		{name: "malformed JSON", call: fakeCall{match: "herdr session list --json", out: []byte(`{`)}},
		{name: "command error", call: fakeCall{match: "herdr session list --json", err: errors.New("daemon down")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New("herdr", WithRunner(&fakeRunner{script: []fakeCall{tt.call}})).ListSessions(context.Background())
			if err == nil {
				t.Fatalf("ListSessions = %+v, want error", got)
			}
			if len(got) != 0 {
				t.Errorf("ListSessions result = %+v, want no sessions on failure", got)
			}
		})
	}
}

func TestDriver_FocusOrCreateUsesDedicatedCommands(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace focus w1", out: []byte(`{}`)},
		{match: "herdr workspace create --cwd /new --label rendered-name --focus", out: []byte(`{"result":{"workspace":{"workspace_id":"w2"},"tab":{"tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"}}}`)},
	}}
	driver := New("herdr", WithRunner(runner))
	focused, err := driver.FocusOrCreate(context.Background(), source.WorkspaceLaunchRequest{Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}}})
	if err != nil || focused.Action != source.HerdrActionFocused || focused.WorkspaceID != "w1" {
		t.Fatalf("FocusOrCreate herdr = (%+v, %v)", focused, err)
	}
	created, err := driver.FocusOrCreate(context.Background(), source.WorkspaceLaunchRequest{Candidate: source.Candidate{Path: "/new", Label: "new"}, WorkspaceName: "rendered-name"})
	if err != nil || created.Action != source.HerdrActionCreated || created.RootPaneID != "w2:p1" {
		t.Fatalf("FocusOrCreate create = (%+v, %v)", created, err)
	}
}

func TestDriver_ImperativeCommandsAndLiveRead(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{
		{match: "herdr tab create --workspace w1 --cwd /work --label editor --focus", out: []byte(`{"result":{"tab":{"tab_id":"w1:t2","workspace_id":"w1","label":"editor"},"root_pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t2"}}}`)},
		{match: "herdr tab rename w1:t2 renamed", out: []byte(`{}`)},
		{match: "herdr pane split w1:p2 --direction right --ratio 0.5 --cwd /work --no-focus", out: []byte(`{"result":{"pane":{"pane_id":"w1:p3","workspace_id":"w1","tab_id":"w1:t2"}}}`)},
		{match: "herdr pane run w1:p3 echo hi", out: []byte(`{}`)},
		{match: "herdr tab focus w1:t2", out: []byte(`{}`)},
		{match: "herdr pane read w1:p3 --lines 2 --format ansi", out: []byte("one\ntwo")},
	}}
	driver := New("herdr", WithRunner(runner))
	tab, pane, err := driver.CreateTab(context.Background(), "w1", "/work", "editor", true)
	if err != nil || tab.ID != "w1:t2" || pane.ID != "w1:p2" {
		t.Fatalf("CreateTab = (%+v, %+v, %v)", tab, pane, err)
	}
	if err := driver.RenameTab(context.Background(), "w1:t2", "renamed"); err != nil {
		t.Fatal(err)
	}
	split, err := driver.SplitPane(context.Background(), "w1:p2", "right", 0.5, "/work", false)
	if err != nil || split.ID != "w1:p3" {
		t.Fatalf("SplitPane = (%+v, %v)", split, err)
	}
	if err := driver.RunPane(context.Background(), "w1:p3", "echo hi"); err != nil {
		t.Fatal(err)
	}
	if err := driver.FocusTab(context.Background(), "w1:t2"); err != nil {
		t.Fatal(err)
	}
	text, err := driver.ReadPane(context.Background(), "w1:p3", 2)
	if err != nil || text != "one\ntwo" {
		t.Fatalf("ReadPane = (%q, %v)", text, err)
	}
}

func TestDriver_RejectsInvalidCreatedPaneIDs(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{{match: "herdr pane split w1:p1 --direction right --ratio 0.5 --no-focus", out: []byte(`{"result":{"pane":{"pane_id":"p1; rm -rf /"}}}`)}}}
	_, err := New("herdr", WithRunner(runner)).SplitPane(context.Background(), "w1:p1", "right", 0.5, "", false)
	if err == nil {
		t.Fatal("SplitPane accepted a malicious pane id")
	}
}
