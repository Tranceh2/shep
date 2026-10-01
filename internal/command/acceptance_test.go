package command

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/history"
	"github.com/tranceh2/shep/internal/source"
)

type acceptanceSnap struct{ snap source.Snapshot }

func (a acceptanceSnap) Snapshot(context.Context) (source.Snapshot, error) { return a.snap, nil }

// singleConnDialer hands out its one connection on the first Dial call, then
// blocks on ctx (rather than reconnecting) on any subsequent call — pointer
// receiver so `used` actually persists across calls, matching
// internal/herdrwatch/acceptance_test.go's oneShotDialer.
type singleConnDialer struct {
	conn net.Conn
	used bool
}

func (d *singleConnDialer) Dial(ctx context.Context) (net.Conn, error) {
	if d.used {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d.used = true
	return d.conn, nil
}

type blockUntilDone struct{}

func (blockUntilDone) Wait(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func startAcceptanceCollector(t *testing.T, snapSrc herdrwatch.SnapshotSource, events ...string) (string, string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "shepacc")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	paths, err := resolveSessionPaths(filepath.Join(dir, "herdr.sock"), dir)
	if err != nil {
		t.Fatalf("resolveSessionPaths: %v", err)
	}

	store, err := history.OpenPath(paths.DBPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv, cli := net.Pipe()
	go func() {
		r := bufio.NewReader(srv)
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		var buf strings.Builder
		buf.WriteString(`{"id":"shep-watch-history","result":{"type":"subscription_started"}}` + "\n")
		for _, ev := range events {
			buf.WriteString(ev + "\n")
		}
		io.WriteString(srv, buf.String())
		io.Copy(io.Discard, srv)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- herdrwatch.Run(ctx, herdrwatch.Config{
			SessionKey:  paths.SessionKey,
			LockPath:    paths.LockPath,
			ControlPath: paths.ControlPath,
			Store:       store,
			Snapshotter: herdrwatch.NewSnapshotAdapter(snapSrc),
			Dialer:      &singleConnDialer{conn: cli},
			Waiter:      blockUntilDone{},
		})
	}()

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("collector did not exit after cancellation")
		}
	}
	return paths.ControlPath, paths.SessionKey, stop
}

func pollControl(ctx context.Context, path, key string, want func(herdrwatch.StateResp) bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, time.Second)
		resp, qerr := herdrwatch.QueryState(qctx, conn, key)
		cancel()
		_ = conn.Close()
		if qerr == nil && want(resp) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestAcceptance_R7S1_ExternalFocusReachesJumpBackEndToEnd(t *testing.T) {
	live := source.Snapshot{
		Workspaces:         []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}},
		FocusedWorkspaceID: "ws-b",
	}
	evA := `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"ws-a"}}`
	evB := `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"ws-b"}}`

	controlPath, sessionKey, stop := startAcceptanceCollector(t, acceptanceSnap{snap: live}, evA, evB)
	defer stop()

	ready := pollControl(context.Background(), controlPath, sessionKey, func(r herdrwatch.StateResp) bool {
		return r.Ready && len(r.MRU) == 2 && r.MRU[0] == "ws-b"
	})
	if !ready {
		t.Fatal("external focus events never produced a ready collector state")
	}

	drv := &jbDriver{snapshots: []source.Snapshot{live, live}}
	var out, errOut bytes.Buffer
	err := runJumpBack(context.Background(), drv, unixControlDialer{path: controlPath}, sessionKey, &out, &errOut)

	if err != nil || drv.focusCalls != 1 || drv.focusedID != "ws-a" {
		t.Fatalf("jump-back failed: err=%v focusCalls=%d focusedID=%q", err, drv.focusCalls, drv.focusedID)
	}
}

func TestAcceptance_R4S2_SnapshotStreamConflictJumpBackFailsClosed(t *testing.T) {
	coherentSnap := source.Snapshot{
		Workspaces:         []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}},
		FocusedWorkspaceID: "ws-a",
	}
	evB := `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"ws-b"}}`
	evCloseA := `{"event":"workspace_closed","data":{"type":"workspace_closed","workspace_id":"ws-a"}}`

	controlPath, sessionKey, stop := startAcceptanceCollector(t, acceptanceSnap{snap: coherentSnap}, evB, evCloseA, evB)
	defer stop()

	unready := pollControl(context.Background(), controlPath, sessionKey, func(r herdrwatch.StateResp) bool {
		return !r.Ready
	})
	if !unready {
		t.Fatal("expected owner to be unready on snapshot/stream conflict")
	}

	drv := &jbDriver{snapshots: []source.Snapshot{{FocusedWorkspaceID: "ws-b"}}}
	var out, errOut bytes.Buffer
	err := runJumpBack(context.Background(), drv, unixControlDialer{path: controlPath}, sessionKey, &out, &errOut)

	if got := ExitCode(err); got != 3 || drv.focusCalls != 0 || !strings.Contains(errOut.String(), "jump-back: history not ready") {
		t.Fatalf("exit=%d (want 3) calls=%d stderr=%q", got, drv.focusCalls, errOut.String())
	}
}

func TestAcceptance_R10S3_OperatorDocsExistAndAreRunnable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "jump-back.md"))
	if err != nil {
		t.Fatalf("operator docs must ship: %v", err)
	}
	for _, cmd := range []string{
		"shep jump-back",
		"herdr plugin link",
		"herdr plugin action invoke start-history --plugin tranceh2.shep",
		"herdr plugin action invoke jump-back --plugin tranceh2.shep",
		"herdr plugin disable tranceh2.shep",
		"herdr plugin unlink  tranceh2.shep",
		"contrib/herdr-plugin",
	} {
		if !bytes.Contains(data, []byte(cmd)) {
			t.Errorf("missing command %q", cmd)
		}
	}
	for _, claim := range []string{
		"No sequence or snapshot-cut marker",
		"Offline history cannot be reconstructed",
		"residual pre-focus race remains",
		"not host death",
		"jump-back: history not ready",
	} {
		if !bytes.Contains(data, []byte(claim)) {
			t.Errorf("missing limit %q", claim)
		}
	}
	if bytes.Contains(data, []byte("lossless")) && !bytes.Contains(data, []byte("No losslessness")) {
		t.Error("docs must not claim losslessness")
	}
}

// TestAcceptance_PluginActionInvokeSyntaxIsExact pins the current Herdr
// CLI syntax across every doc that tells an operator how to invoke a
// plugin action manually: the bare action ID (never the fully-qualified
// tranceh2.shep.<id> form used in keybinding `command =` values) plus
// `--plugin tranceh2.shep`. A regression here is silent until an operator
// copy-pastes a doc command that Herdr rejects.
func TestAcceptance_PluginActionInvokeSyntaxIsExact(t *testing.T) {
	staleForms := []string{
		"herdr plugin action invoke tranceh2.shep.open",
		"herdr plugin action invoke tranceh2.shep.jump-back",
		"herdr plugin action invoke tranceh2.shep.start-history",
		"herdr plugin action invoke tranceh2.shep.doctor",
	}
	for _, doc := range []string{
		filepath.Join("..", "..", "docs", "herdr-plugin.md"),
		filepath.Join("..", "..", "docs", "jump-back.md"),
		filepath.Join("..", "..", "README.md"),
	} {
		data, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("read %s: %v", doc, err)
		}
		for _, stale := range staleForms {
			if bytes.Contains(data, []byte(stale)) {
				t.Errorf("%s: contains stale fully-qualified invoke syntax %q; action invoke takes the bare action ID plus --plugin tranceh2.shep", doc, stale)
			}
		}
	}
	// herdr-plugin.md documents all four actions and must show the exact
	// verified syntax for each.
	pluginDoc, err := os.ReadFile(filepath.Join("..", "..", "docs", "herdr-plugin.md"))
	if err != nil {
		t.Fatalf("read docs/herdr-plugin.md: %v", err)
	}
	for _, cmd := range []string{
		"herdr plugin action invoke open --plugin tranceh2.shep",
		"herdr plugin action invoke jump-back --plugin tranceh2.shep",
		"herdr plugin action invoke start-history --plugin tranceh2.shep",
		"herdr plugin action invoke doctor --plugin tranceh2.shep",
	} {
		if !bytes.Contains(pluginDoc, []byte(cmd)) {
			t.Errorf("docs/herdr-plugin.md: missing exact invoke command %q", cmd)
		}
	}
}

// TestAcceptance_ReadmeExplainsPluginActionDoesNotRequireShepLink pins the
// README claim that type="plugin_action" keybindings work from the Herdr
// plugin install alone, and that `shep link` is a separate, optional step.
func TestAcceptance_ReadmeExplainsPluginActionDoesNotRequireShepLink(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	for _, claim := range []string{
		"shep link` is NOT required",
		"`shep link` is **optional**",
	} {
		if !bytes.Contains(data, []byte(claim)) {
			t.Errorf("README.md: missing claim %q", claim)
		}
	}
}

// TestAcceptance_FirstRunChecklistVerifiesWithPluginDoctor pins the first-run
// checklist's internally consistent verification path: the default doctor run
// is the plugin action (usable with the plugin install alone, no bare `shep`
// on PATH), while `shep doctor` appears only as an equivalent for an
// operator who separately installed or linked the CLI. A bare `shep` is
// unavailable in a plugin-only install, so a first-run checklist defaulting
// to it would fail every fresh operator.
func TestAcceptance_FirstRunChecklistVerifiesWithPluginDoctor(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	const firstRunSection = "## Recommended First-Run Setup"
	idx := bytes.Index(data, []byte(firstRunSection))
	if idx < 0 {
		t.Fatal("README.md: missing Recommended First-Run Setup section")
	}
	section := data[idx:]
	if end := bytes.Index(section, []byte("\n## ")); end >= 0 {
		section = section[:end]
	}
	for _, claim := range []string{
		"herdr plugin action invoke doctor --plugin tranceh2.shep",
		"If you separately installed or linked the `shep` CLI",
		"`shep doctor` is an",
	} {
		if !bytes.Contains(section, []byte(claim)) {
			t.Errorf("README.md first-run section: missing claim %q", claim)
		}
	}
	// Within the first-run section, a checklist/step default of `shep doctor`
	// is the error this pins against: the section must not anywhere name
	// `shep doctor` before establishing it as the linked-CLI equivalent.
	intro := section[:bytes.Index(section, []byte("`shep doctor` is an"))]
	if before := bytes.Index(intro, []byte("`shep doctor`")); before >= 0 && before < bytes.Index(intro, []byte("If you separately installed")) {
		t.Errorf("README.md first-run section: `shep doctor` must not appear before the linked-CLI equivalent caveat:\n%s", intro)
	}
}
