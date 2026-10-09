package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/source"
)

// controlDialerFunc adapts a function to controlDialer.
type controlDialerFunc func(ctx context.Context) (net.Conn, error)

func (f controlDialerFunc) Dial(ctx context.Context) (net.Conn, error) { return f(ctx) }

// jbDriver is the narrow snapshot+focus surface jump-back consumes. Scripted
// snapshots are returned in order so the two independent validation reads
// (resolution and pre-focus) can diverge, which is what the race guards need.
type jbDriver struct {
	snapshots []source.Snapshot
	snapErr   error
	snapCalls int

	focusErr    error
	focusedID   string
	focusSource string
	focusCalls  int
}

func (d *jbDriver) Snapshot(context.Context) (source.Snapshot, error) {
	d.snapCalls++
	if d.snapErr != nil {
		return source.Snapshot{}, d.snapErr
	}
	if len(d.snapshots) == 0 {
		return source.Snapshot{}, errors.New("jbDriver: no scripted snapshot")
	}
	i := d.snapCalls - 1
	if i >= len(d.snapshots) {
		i = len(d.snapshots) - 1
	}
	return d.snapshots[i], nil
}

func (d *jbDriver) FocusOrCreate(_ context.Context, req source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	d.focusCalls++
	d.focusSource = req.Candidate.Source
	d.focusedID = req.Candidate.Meta["workspace_id"]
	if d.focusErr != nil {
		return source.FocusResult{}, d.focusErr
	}
	return source.FocusResult{WorkspaceID: d.focusedID, Action: source.HerdrActionFocused}, nil
}

// snapOf builds a snapshot with the given live workspace ids and focused id.
func snapOf(focused string, ids ...string) source.Snapshot {
	ws := make([]source.Workspace, 0, len(ids))
	for _, id := range ids {
		ws = append(ws, source.Workspace{ID: id, Focused: id == focused})
	}
	return source.Snapshot{Workspaces: ws, FocusedWorkspaceID: focused}
}

// controlDialerFor serves exactly one StateResp over an in-memory pipe, which
// keeps the test hermetic (no filesystem socket, no live owner).
func controlDialerFor(t *testing.T, resp herdrwatch.StateResp) controlDialer {
	t.Helper()
	return controlDialerFunc(func(context.Context) (net.Conn, error) {
		server, client := net.Pipe()
		go func() {
			defer server.Close()
			srv := herdrwatch.NewServer(staticProvider{resp: resp})
			_ = srv.HandleConn(context.Background(), server)
		}()
		return client, nil
	})
}

type staticProvider struct{ resp herdrwatch.StateResp }

func (p staticProvider) State(context.Context, string) (herdrwatch.StateResp, error) {
	return p.resp, nil
}

// hungControlDialer returns a connection whose peer never answers, modelling a
// hung owner. QueryState must time out and the CLI must fail closed.
func hungControlDialer(t *testing.T) controlDialer {
	t.Helper()
	return controlDialerFunc(func(context.Context) (net.Conn, error) {
		_, client := net.Pipe()
		return client, nil
	})
}

func readyResp(mru ...string) herdrwatch.StateResp {
	return herdrwatch.StateResp{Ready: true, Epoch: 3, MRU: mru}
}

func TestJumpBack_PreviousDistinctFocusesLiveTarget(t *testing.T) {
	drv := &jbDriver{snapshots: []source.Snapshot{
		snapOf("ws-b", "ws-a", "ws-b"),
		snapOf("ws-b", "ws-a", "ws-b"),
	}}
	var out, errOut bytes.Buffer

	err := runJumpBack(context.Background(), drv, controlDialerFor(t, readyResp("ws-b", "ws-a")), "key-1", &out, &errOut)

	if err != nil {
		t.Fatalf("expected success, got %v (stderr=%q)", err, errOut.String())
	}
	if ExitCode(err) != 0 {
		t.Fatalf("expected exit 0, got %d", ExitCode(err))
	}
	if drv.focusCalls != 1 {
		t.Fatalf("expected exactly 1 focus call, got %d", drv.focusCalls)
	}
	if drv.focusedID != "ws-a" {
		t.Fatalf("expected focus on ws-a, got %q", drv.focusedID)
	}
	if drv.focusSource != config.SourceHerdr {
		t.Fatalf("expected SourceHerdr (focus without creation), got %q", drv.focusSource)
	}
	if drv.snapCalls != 2 {
		t.Fatalf("expected 2 fresh snapshots (resolve + pre-focus), got %d", drv.snapCalls)
	}
}

func TestJumpBack_ExitCodeTaxonomy(t *testing.T) {
	tests := []struct {
		name     string
		dialer   func(*testing.T) controlDialer
		driver   *jbDriver
		wantCode int
		wantMsg  string
	}{
		{
			name:     "no history when mru holds only the current workspace",
			dialer:   func(t *testing.T) controlDialer { return controlDialerFor(t, readyResp("ws-b")) },
			driver:   &jbDriver{snapshots: []source.Snapshot{snapOf("ws-b", "ws-b")}},
			wantCode: 2,
			wantMsg:  "no previous workspace",
		},
		{
			name: "not ready when the owner reports an unverified epoch",
			dialer: func(t *testing.T) controlDialer {
				return controlDialerFor(t, herdrwatch.StateResp{Ready: false, MRU: []string{"ws-b", "ws-a"}})
			},
			driver:   &jbDriver{snapshots: []source.Snapshot{snapOf("ws-b", "ws-a", "ws-b")}},
			wantCode: 3,
			wantMsg:  "history not ready",
		},
		{
			name: "not ready when no collector answers the control socket",
			dialer: func(t *testing.T) controlDialer {
				return controlDialerFunc(func(context.Context) (net.Conn, error) {
					return nil, errors.New("dial unix: connect: no such file or directory")
				})
			},
			driver:   &jbDriver{},
			wantCode: 3,
			wantMsg:  "history not ready",
		},
		{
			name:     "hung owner fails closed as not ready",
			dialer:   hungControlDialer,
			driver:   &jbDriver{},
			wantCode: 3,
			wantMsg:  "history not ready",
		},
		{
			name: "store error is classified without leaking internals",
			dialer: func(t *testing.T) controlDialer {
				return controlDialerFor(t, herdrwatch.StateResp{Ready: false, ClassifiedError: "history store error"})
			},
			driver:   &jbDriver{},
			wantCode: 6,
			wantMsg:  "history store error",
		},
		{
			name:     "session unavailable when the target vanishes before focus",
			dialer:   func(t *testing.T) controlDialer { return controlDialerFor(t, readyResp("ws-b", "ws-a")) },
			driver:   &jbDriver{snapshots: []source.Snapshot{snapOf("ws-b", "ws-a", "ws-b"), snapOf("ws-b", "ws-b")}},
			wantCode: 4,
			wantMsg:  "target session no longer available",
		},
		{
			name:     "current changed between resolve and focus",
			dialer:   func(t *testing.T) controlDialer { return controlDialerFor(t, readyResp("ws-b", "ws-a")) },
			driver:   &jbDriver{snapshots: []source.Snapshot{snapOf("ws-b", "ws-a", "ws-b"), snapOf("ws-c", "ws-a", "ws-b", "ws-c")}},
			wantCode: 5,
			wantMsg:  "current workspace changed",
		},
		{
			name:     "owner state disagreeing with the fresh snapshot refuses",
			dialer:   func(t *testing.T) controlDialer { return controlDialerFor(t, readyResp("ws-b", "ws-a")) },
			driver:   &jbDriver{snapshots: []source.Snapshot{snapOf("ws-c", "ws-a", "ws-b", "ws-c")}},
			wantCode: 5,
			wantMsg:  "current workspace changed",
		},
		{
			name:     "snapshot failure refuses instead of focusing blind",
			dialer:   func(t *testing.T) controlDialer { return controlDialerFor(t, readyResp("ws-b", "ws-a")) },
			driver:   &jbDriver{snapErr: errors.New("herdr api snapshot: exit status 1")},
			wantCode: 4,
			wantMsg:  "target session no longer available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := runJumpBack(context.Background(), tt.driver, tt.dialer(t), "key-1", &out, &errOut)

			if got := ExitCode(err); got != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (err=%v, stderr=%q)", got, tt.wantCode, err, errOut.String())
			}
			if !strings.Contains(errOut.String(), tt.wantMsg) {
				t.Fatalf("stderr = %q, want it to contain %q", errOut.String(), tt.wantMsg)
			}
			if tt.driver.focusCalls != 0 {
				t.Fatalf("expected no focus invocation on a refusal, got %d", tt.driver.focusCalls)
			}
		})
	}
}

// TestJumpBack_FocusFailureSurfacedAsSanitizedClassification resolves the
// R7.S4-vs-design tension technically. R7.S4 requires the focus failure to be
// surfaced (non-zero exit, user told focus failed). The design's final
// constraint requires all user-facing errors to be sanitized classifications
// rather than verbatim internal payloads. Both hold when stderr carries a
// stable "focus failed" classification while the driver's error stays in the
// returned error chain for programmatic observability.
func TestJumpBack_FocusFailureSurfacedAsSanitizedClassification(t *testing.T) {
	driverErr := errors.New("herdr workspace focus ws-a: exit status 1")
	drv := &jbDriver{
		snapshots: []source.Snapshot{snapOf("ws-b", "ws-a", "ws-b"), snapOf("ws-b", "ws-a", "ws-b")},
		focusErr:  driverErr,
	}
	var out, errOut bytes.Buffer

	err := runJumpBack(context.Background(), drv, controlDialerFor(t, readyResp("ws-b", "ws-a")), "key-1", &out, &errOut)

	if got := ExitCode(err); got == 0 {
		t.Fatalf("expected a non-zero exit when focus fails, got %d", got)
	}
	if drv.focusCalls != 1 {
		t.Fatalf("expected the focus attempt to have happened once, got %d", drv.focusCalls)
	}
	if !strings.Contains(errOut.String(), "focus failed") {
		t.Fatalf("stderr = %q, want it to surface that the focus operation failed", errOut.String())
	}
	if !errors.Is(err, driverErr) {
		t.Fatalf("expected the driver error to remain in the returned chain, got %v", err)
	}
}

// TestJumpBack_FocusErrorPayloadNeverLeaksToStderr is the negative control for
// the sanitization half. A driver error may embed an arbitrary payload (here a
// credential-looking string from a command line). It must never be echoed to
// the user, while the failure itself is still reported non-zero.
func TestJumpBack_FocusErrorPayloadNeverLeaksToStderr(t *testing.T) {
	const secret = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG"
	drv := &jbDriver{
		snapshots: []source.Snapshot{snapOf("ws-b", "ws-a", "ws-b"), snapOf("ws-b", "ws-a", "ws-b")},
		focusErr:  fmt.Errorf("herdr workspace focus ws-a: exec env %s: exit status 1", secret),
	}
	var out, errOut bytes.Buffer

	err := runJumpBack(context.Background(), drv, controlDialerFor(t, readyResp("ws-b", "ws-a")), "key-1", &out, &errOut)

	if got := ExitCode(err); got == 0 {
		t.Fatalf("expected a non-zero exit when focus fails, got %d", got)
	}
	if strings.Contains(errOut.String(), secret) {
		t.Fatalf("stderr leaked the internal error payload: %q", errOut.String())
	}
	if strings.Contains(errOut.String(), "wJalrXUtnFEMI") {
		t.Fatalf("stderr leaked a credential fragment: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "focus failed") {
		t.Fatalf("stderr = %q, want the sanitized focus-failure classification", errOut.String())
	}
}

func TestResolvePreviousDistinct(t *testing.T) {
	live := func(ids ...string) map[string]struct{} {
		m := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			m[id] = struct{}{}
		}
		return m
	}

	tests := []struct {
		name    string
		mru     []string
		current string
		live    map[string]struct{}
		want    string
		wantOK  bool
	}{
		{
			name:    "previous distinct entry after the current head",
			mru:     []string{"ws-b", "ws-a"},
			current: "ws-b",
			live:    live("ws-a", "ws-b"),
			want:    "ws-a",
			wantOK:  true,
		},
		{
			name:    "consecutive duplicates collapse to the next distinct workspace",
			mru:     []string{"ws-a", "ws-b"},
			current: "ws-a",
			live:    live("ws-a", "ws-b"),
			want:    "ws-b",
			wantOK:  true,
		},
		{
			name:    "dead workspaces are skipped in favour of a live one",
			mru:     []string{"ws-a", "ws-gone", "ws-c"},
			current: "ws-a",
			live:    live("ws-a", "ws-c"),
			want:    "ws-c",
			wantOK:  true,
		},
		{
			name:    "unsafe workspace ids are never selected",
			mru:     []string{"ws-a", "; rm -rf /", "ws-c"},
			current: "ws-a",
			live:    live("ws-a", "; rm -rf /", "ws-c"),
			want:    "ws-c",
			wantOK:  true,
		},
		{
			name:    "only the current workspace means no previous target",
			mru:     []string{"ws-a", "ws-a"},
			current: "ws-a",
			live:    live("ws-a"),
			want:    "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolvePreviousDistinct(tt.mru, tt.current, tt.live)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.wantOK, got)
			}
			if got != tt.want {
				t.Fatalf("target = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveSessionPaths_PerSocketIsolation(t *testing.T) {
	root := t.TempDir()

	p1, err := resolveSessionPaths("/tmp/herdr-one.sock", root)
	if err != nil {
		t.Fatalf("resolveSessionPaths(one): %v", err)
	}
	p2, err := resolveSessionPaths("/tmp/herdr-two.sock", root)
	if err != nil {
		t.Fatalf("resolveSessionPaths(two): %v", err)
	}

	if p1.SessionKey == "" || p1.SessionKey == p2.SessionKey {
		t.Fatalf("expected distinct non-empty session keys, got %q and %q", p1.SessionKey, p2.SessionKey)
	}
	if p1.ControlPath == p2.ControlPath || p1.LockPath == p2.LockPath {
		t.Fatalf("expected per-socket control/lock paths, got %+v and %+v", p1, p2)
	}
	if p1.DBPath != p2.DBPath {
		t.Fatalf("expected one shared history database keyed by session, got %q and %q", p1.DBPath, p2.DBPath)
	}
	if !strings.HasPrefix(p1.ControlPath, root) || !strings.HasSuffix(p1.ControlPath, ".sock") {
		t.Fatalf("control path %q is not a .sock under the state root %q", p1.ControlPath, root)
	}
	if _, err := resolveSessionPaths("", root); err == nil {
		t.Fatal("expected an error when no Herdr socket path is available")
	}
}

// TestJumpBackCmd_MissingSocketExitsSingleLineCodeThree exercises the real
// jumpBackCmd() command path (via App.executeArgs, not the runJumpBack
// helper) with no HERDR_SOCKET_PATH set. It pins the full command-level
// contract in one assertion: exactly one sanitized stderr line, exit code 3
// (exitNotReady), and no generic "error:" fallback wrapper — i.e. the whole
// markReported chain from jumpBackCmd's inline refusal through Execute's
// fallback holds end to end, not just at the runJumpBack helper layer.
func TestJumpBackCmd_MissingSocketExitsSingleLineCodeThree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut), WithHerdrDriver(&openDriver{}))

	err := app.executeArgs([]string{"jump-back"})

	if err == nil {
		t.Fatal("expected a non-nil error with no Herdr socket configured")
	}
	if got := ExitCode(err); got != exitNotReady {
		t.Fatalf("exit code = %d, want %d (exitNotReady)", got, exitNotReady)
	}
	stderr := errOut.String()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stderr = %q, want exactly one line", stderr)
	}
	if strings.Contains(stderr, "error: ") {
		t.Fatalf("stderr = %q, want no generic 'error:' fallback wrapper", stderr)
	}
	if !strings.Contains(stderr, "jump-back:") {
		t.Fatalf("stderr = %q, want the jump-back-prefixed sanitized diagnostic", stderr)
	}
}

func TestJumpBack_ControlQueryHonoursContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	var out, errOut bytes.Buffer
	start := time.Now()
	err := runJumpBack(ctx, &jbDriver{}, hungControlDialer(t), "key-1", &out, &errOut)
	elapsed := time.Since(start)

	if got := ExitCode(err); got != 3 {
		t.Fatalf("exit code = %d, want 3 for a hung owner", got)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("expected a bounded control query, took %s", elapsed)
	}
}
