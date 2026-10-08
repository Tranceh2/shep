package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// cliMustNotRun fails the test when the driver launches the CLI: with a
// socket, every server request must go over it.
type cliMustNotRun struct{ t *testing.T }

func (r cliMustNotRun) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.t.Errorf("CLI launched: %s %v", name, args)
	return nil, errors.New("CLI disabled in this test")
}

// replyWith answers every request with result under the request's own id.
func replyWith(result string) respondFn {
	return func(raw []byte) []byte {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &req)
		return []byte(`{"id":` + strconv.Quote(req.ID) + `,"result":` + result + "}\n")
	}
}

// decodeJSON decodes s into a generic value so two encodings compare by
// content, not by key order.
func decodeJSON(t *testing.T, s []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(s, &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

const (
	createdResult  = `{"type":"workspace_created","workspace":{"workspace_id":"w9"},"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"}}`
	tabResult      = `{"type":"tab_created","tab":{"tab_id":"w1:t2","workspace_id":"w1"},"root_pane":{"pane_id":"w1:p4"}}`
	splitResult    = `{"type":"pane_info","pane":{"pane_id":"w1:p5"}}`
	worktreeResult = `{"type":"worktree_created","workspace":{"workspace_id":"w9","label":"feat","worktree":{"repo_name":"app"}},"tab":{"tab_id":"w9:t1"},"root_pane":{"pane_id":"w9:p1"},"worktree":{"path":"/r/app-worktrees/feat","branch":"feat"}}`
	okResult       = `{"type":"ok"}`
)

// TestDriver_SocketRequestsMatchTheCLI proves each driver call sends, over the
// socket, the exact method and params the herdr CLI sends for the same
// command (captured from the CLI against a fake server), and never launches
// the CLI.
func TestDriver_SocketRequestsMatchTheCLI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	label := "logs"
	clear := ""
	for _, tc := range []struct {
		name   string
		result string
		call   func(*Driver) error
		want   string
	}{
		{"snapshot", `{"type":"session_snapshot","snapshot":{}}`, func(d *Driver) error { _, err := d.Snapshot(ctx); return err },
			`{"method":"session.snapshot","params":{}}`},
		{"focus an open workspace", okResult, func(d *Driver) error {
			_, err := d.FocusOrCreate(ctx, source.WorkspaceLaunchRequest{Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}}})
			return err
		}, `{"method":"workspace.focus","params":{"workspace_id":"w1"}}`},
		{"create a workspace", createdResult, func(d *Driver) error {
			_, err := d.FocusOrCreate(ctx, source.WorkspaceLaunchRequest{Candidate: source.Candidate{Source: config.SourceZoxide, Path: "/r/app", NormalizedPath: "/r/app"}, WorkspaceName: "app"})
			return err
		}, `{"method":"workspace.create","params":{"cwd":"/r/app","focus":true,"label":"app"}}`},
		{"create a tab", tabResult, func(d *Driver) error { _, _, err := d.CreateTab(ctx, "w1", "/r", "t", false); return err },
			`{"method":"tab.create","params":{"workspace_id":"w1","cwd":"/r","focus":false,"label":"t"}}`},
		{"rename a tab", okResult, func(d *Driver) error { return d.RenameTab(ctx, "w1:t1", "newname") },
			`{"method":"tab.rename","params":{"tab_id":"w1:t1","label":"newname"}}`},
		{"rename a pane", okResult, func(d *Driver) error { return d.RenamePane(ctx, "w1:p1", &label) },
			`{"method":"pane.rename","params":{"pane_id":"w1:p1","label":"logs"}}`},
		{"clear a pane label", okResult, func(d *Driver) error { return d.RenamePane(ctx, "w1:p1", &clear) },
			`{"method":"pane.rename","params":{"pane_id":"w1:p1"}}`},
		{"split a pane", splitResult, func(d *Driver) error { _, err := d.SplitPane(ctx, "w1:p1", "right", 0.6, "/r", false); return err },
			`{"method":"pane.split","params":{"target_pane_id":"w1:p1","direction":"right","ratio":0.6,"cwd":"/r","focus":false,"right_click":"herdr"}}`},
		{"run in a pane", okResult, func(d *Driver) error { return d.RunPane(ctx, "w1:p1", "make test") },
			`{"method":"pane.send_input","params":{"pane_id":"w1:p1","text":"make test","keys":["Enter"]}}`},
		{"focus a tab", okResult, func(d *Driver) error { return d.FocusTab(ctx, "w1:t1") },
			`{"method":"tab.focus","params":{"tab_id":"w1:t1"}}`},
		{"close a pane", okResult, func(d *Driver) error { return d.ClosePane(ctx, "w1:p1") },
			`{"method":"pane.close","params":{"pane_id":"w1:p1"}}`},
		{"close a tab", okResult, func(d *Driver) error { return d.CloseTab(ctx, "w1:t1") },
			`{"method":"tab.close","params":{"tab_id":"w1:t1"}}`},
		{"close a workspace", okResult, func(d *Driver) error { return d.CloseWorkspace(ctx, "w1") },
			`{"method":"workspace.close","params":{"workspace_id":"w1"}}`},
		{"read a pane", `{"type":"pane_read","read":{"text":"x"}}`, func(d *Driver) error { _, err := d.ReadPane(ctx, "w1:p1", 50); return err },
			`{"method":"pane.read","params":{"pane_id":"w1:p1","source":"recent","lines":50,"format":"ansi","strip_ansi":true}}`},
		{"rename a workspace", okResult, func(d *Driver) error { return d.RenameWorkspace(ctx, "w1", "nuevo") },
			`{"method":"workspace.rename","params":{"workspace_id":"w1","label":"nuevo"}}`},
		{"create a worktree", worktreeResult, func(d *Driver) error { _, err := d.CreateWorktree(ctx, "/r/app", "feat"); return err },
			`{"method":"worktree.create","params":{"cwd":"/r/app","branch":"feat","focus":true}}`},
		{"open the picker popup", okResult, func(d *Driver) error { return d.OpenPluginPane(ctx, "tranceh2.shep", "picker", "popup") },
			`{"method":"plugin.pane.open","params":{"plugin_id":"tranceh2.shep","entrypoint":"picker","placement":"popup","focus":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newMockHerdrServer(t, replyWith(tc.result))
			d := New("herdr", WithRunner(cliMustNotRun{t}), WithSocketPath(srv.path))
			if err := tc.call(d); err != nil {
				t.Fatalf("call: %v", err)
			}
			reqs := srv.gotRequests()
			if len(reqs) != 1 {
				t.Fatalf("server saw %d requests, want 1", len(reqs))
			}
			got := decodeJSON(t, reqs[0]).(map[string]any)
			delete(got, "id")
			if want := decodeJSON(t, []byte(tc.want)); !reflect.DeepEqual(got, want) {
				t.Errorf("request = %v\nwant      %v", got, want)
			}
		})
	}
}

// TestDriver_SocketResultsDecodeLikeTheCLI proves the socket path decodes the
// same results the CLI path does: the snapshot's records, the pane buffer
// (result.read.text, which the CLI prints as plain stdout), and a created
// worktree's workspace and path.
func TestDriver_SocketResultsDecodeLikeTheCLI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := newMockHerdrServer(t, replyWith(`{"type":"session_snapshot","snapshot":{"workspaces":[{"workspace_id":"w1","label":"api","focused":true}],"focused_workspace_id":"w1"}}`))
	snap, err := New("herdr", WithRunner(cliMustNotRun{t}), WithSocketPath(srv.path)).Snapshot(ctx)
	if err != nil || len(snap.Workspaces) != 1 || snap.Workspaces[0].Label != "api" || snap.FocusedWorkspaceID != "w1" {
		t.Errorf("Snapshot = %+v, %v; want workspace api focused", snap, err)
	}

	srv = newMockHerdrServer(t, replyWith(`{"type":"pane_read","read":{"pane_id":"w1:p1","text":"\u001b[31mred\u001b[0m\n$ "}}`))
	text, err := New("herdr", WithRunner(cliMustNotRun{t}), WithSocketPath(srv.path)).ReadPane(ctx, "w1:p1", 0)
	if err != nil || text != "\x1b[31mred\x1b[0m\n$ " {
		t.Errorf("ReadPane = %q, %v; want the colored buffer", text, err)
	}

	srv = newMockHerdrServer(t, replyWith(worktreeResult))
	wt, err := New("herdr", WithRunner(cliMustNotRun{t}), WithSocketPath(srv.path)).CreateWorktree(ctx, "/r/app", "feat")
	want := CreatedWorktree{WorkspaceID: "w9", WorkspaceLabel: "feat", RootTabID: "w9:t1", RootPaneID: "w9:p1", Path: "/r/app-worktrees/feat", Branch: "feat", RepoName: "app"}
	if err != nil || wt != want {
		t.Errorf("CreateWorktree = %+v, %v; want %+v", wt, err, want)
	}
}

// TestDriver_OlderHerdrFallsBackToTheCLI proves a server that does not know a
// method (answered with invalid_request and an empty id, as Herdr does) sends
// the request to the CLI of the same Herdr instead.
func TestDriver_OlderHerdrFallsBackToTheCLI(t *testing.T) {
	t.Parallel()
	srv := newMockHerdrServer(t, func([]byte) []byte {
		return []byte(`{"id":"","error":{"code":"invalid_request","message":"invalid request: unknown variant"}}` + "\n")
	})
	runner := &fakeRunner{script: []fakeCall{{match: "herdr api snapshot", out: []byte(`{"id":"cli:api:snapshot","result":{"type":"session_snapshot","snapshot":{"workspaces":[{"workspace_id":"w1","label":"api"}]}}}`)}}}
	snap, err := New("herdr", WithRunner(runner), WithSocketPath(srv.path)).Snapshot(context.Background())
	if err != nil || len(snap.Workspaces) != 1 || snap.Workspaces[0].ID != "w1" {
		t.Fatalf("Snapshot = %+v, %v; want the CLI's snapshot", snap, err)
	}
	if len(runner.calls) != 1 {
		t.Errorf("CLI calls = %v, want exactly the snapshot", runner.calls)
	}
}

// TestDriver_SocketRejectionIsFinal proves a rejection the server reached a
// verdict on (any code but invalid_request) is returned as is, with its code,
// and is not retried on the CLI.
func TestDriver_SocketRejectionIsFinal(t *testing.T) {
	t.Parallel()
	srv := newMockHerdrServer(t, func(raw []byte) []byte {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &req)
		return []byte(`{"id":` + strconv.Quote(req.ID) + `,"error":{"code":"workspace_group_close_required","message":"close the group"}}` + "\n")
	})
	err := New("herdr", WithRunner(cliMustNotRun{t}), WithSocketPath(srv.path)).CloseWorkspace(context.Background(), "w1")
	var rpcErr *HerdrRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != "workspace_group_close_required" {
		t.Fatalf("CloseWorkspace error = %v, want the server's workspace_group_close_required rejection", err)
	}
}

// TestDriver_UnreachableSocketDoesNotLaunchTheCLI proves a socket that cannot
// be dialed fails the request instead of launching the CLI, which would read
// the same HERDR_SOCKET_PATH and fail the same way, only slower.
func TestDriver_UnreachableSocketDoesNotLaunchTheCLI(t *testing.T) {
	t.Parallel()
	_, err := New("herdr", WithRunner(cliMustNotRun{t}), WithSocketPath(t.TempDir()+"/missing.sock")).Snapshot(context.Background())
	if !errors.Is(err, ErrHerdrSocketUnavailable) {
		t.Fatalf("Snapshot error = %v, want ErrHerdrSocketUnavailable", err)
	}
}
