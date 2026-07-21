package herdr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

type fakeRunner struct {
	script []fakeCall
	calls  []string
}

type fakeCall struct {
	match string
	out   []byte
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, key)
	for i, call := range f.script {
		if call.match == key {
			f.script = append(f.script[:i], f.script[i+1:]...)
			return call.out, call.err
		}
	}
	return nil, errors.New("not scripted: " + key)
}

func TestDriverSnapshot_ParsesOnlyFullGeneration(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{{match: "herdr api snapshot", out: []byte(`{
"id":"cli:api:snapshot","result":{"snapshot":{
"focused_workspace_id":"w1","focused_tab_id":"w1:t1","focused_pane_id":"w1:p1",
"workspaces":[{"workspace_id":"w1","label":"project","active_tab_id":"w1:t1","focused":true},{"label":"discard"}],
"tabs":[{"tab_id":"w1:t1","workspace_id":"w1","label":null,"number":1,"pane_count":1},{"workspace_id":"w1"}],
"panes":[{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":"/project","agent_status":"working","unknown":true},{"workspace_id":"w1"}]
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
	if snapshot.Panes[0].AgentStatus != "working" || snapshot.FocusedPaneID != "w1:p1" {
		t.Errorf("snapshot = %+v, want focused working pane", snapshot)
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

func TestDriver_FocusOrCreateUsesDedicatedCommands(t *testing.T) {
	runner := &fakeRunner{script: []fakeCall{
		{match: "herdr workspace focus w1", out: []byte(`{}`)},
		{match: "herdr workspace create --cwd /new --label new --focus", out: []byte(`{"result":{"workspace":{"workspace_id":"w2"},"tab":{"tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"}}}`)},
	}}
	driver := New("herdr", WithRunner(runner))
	focused, err := driver.FocusOrCreate(context.Background(), source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}})
	if err != nil || focused.Action != source.HerdrActionFocused || focused.WorkspaceID != "w1" {
		t.Fatalf("FocusOrCreate herdr = (%+v, %v)", focused, err)
	}
	created, err := driver.FocusOrCreate(context.Background(), source.Candidate{Path: "/new", Label: "new"})
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
