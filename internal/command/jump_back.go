package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/history"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/source"
)

// Exit codes for the jump-back error taxonomy (R8). Each category is
// distinguishable by exit code and by a sanitized user-facing message; no
// internal payload is ever echoed verbatim.
const (
	exitNoHistory          = 2
	exitNotReady           = 3
	exitSessionUnavailable = 4
	exitCurrentChanged     = 5
	exitStoreError         = 6
)

// controlQueryTimeout bounds the control-socket round trip. A hung owner
// therefore fails closed as "not ready" rather than blocking the CLI.
const controlQueryTimeout = 2 * time.Second

// validWorkspaceID mirrors the identifier shape Herdr emits. A history entry
// that does not match is never passed to the driver, so a hostile or corrupt
// stored id cannot reach the command boundary.
var validWorkspaceID = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

// jumpBackDriver is the narrow slice of source.HerdrDriver jump-back needs:
// fresh state validation plus the existing focus operation. Keeping it narrow
// makes the command testable without a full 12-method driver double.
type jumpBackDriver interface {
	Snapshot(ctx context.Context) (source.Snapshot, error)
	FocusOrCreate(ctx context.Context, request source.WorkspaceLaunchRequest) (source.FocusResult, error)
}

// controlDialer opens a connection to the collector's control endpoint. It is a
// seam so tests exercise ready, unready, hung, and absent owners without a
// live collector.
type controlDialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

type controlDialerFunc func(ctx context.Context) (net.Conn, error)

func (f controlDialerFunc) Dial(ctx context.Context) (net.Conn, error) { return f(ctx) }

// unixControlDialer dials the owner's Unix control endpoint.
type unixControlDialer struct{ path string }

func (d unixControlDialer) Dial(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", d.path)
}

// sessionPaths are the per-socket locations shared by `jump-back` and
// `watch-history`. Both commands derive them through resolveSessionPaths so the
// CLI and the collector can never disagree about where the control endpoint,
// the ownership lock, or the history database live.
type sessionPaths struct {
	SessionKey  string
	ControlPath string
	LockPath    string
	DBPath      string
}

// resolveSessionPaths derives the canonical per-socket paths from the Herdr
// socket path. The session key is the full canonical-path hash; the control and
// lock file names use a short prefix of it to stay inside the AF_UNIX path
// limit, which PrepareControlPath validates rather than truncates.
func resolveSessionPaths(socketPath, stateRoot string) (sessionPaths, error) {
	if socketPath == "" {
		return sessionPaths{}, errors.New("no herdr socket path available (HERDR_SOCKET_PATH unset)")
	}
	key, err := history.CanonicalSessionKey(socketPath)
	if err != nil {
		return sessionPaths{}, fmt.Errorf("derive session key: %w", err)
	}
	short := key[:12]
	return sessionPaths{
		SessionKey:  key,
		ControlPath: filepath.Join(stateRoot, "jb-"+short+".sock"),
		LockPath:    filepath.Join(stateRoot, "jb-"+short+".lock"),
		DBPath:      filepath.Join(stateRoot, "jump_history.sqlite3"),
	}, nil
}

// defaultStateRoot is the shep state directory holding the history database and
// the per-socket control/lock files.
func defaultStateRoot() (string, error) {
	return pathutil.StatePath("shep")
}

// currentHerdrSocketPath reports the Herdr socket of the current session, which
// Herdr injects into panes and plugin commands alike. Both jump-back and
// watch-history read it from here so they always agree on the session.
func currentHerdrSocketPath() string {
	return os.Getenv("HERDR_SOCKET_PATH")
}

// jumpBackCmd is the public previous-distinct navigation command.
func (a *App) jumpBackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "jump-back",
		Short: "Focus the previous distinct workspace for the current Herdr socket",
		Long: "jump-back asks the running watch-history collector for this socket's\n" +
			"focus history, revalidates the resolved target against a fresh Herdr\n" +
			"snapshot, and focuses it. It refuses rather than guessing when history\n" +
			"is not ready or the live state changed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			driver := a.pluginDriver()
			jumpDriver, ok := driver.(jumpBackDriver)
			if !ok || driver == nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "jump-back: herdr is unavailable")
				return markReported(&ExitCodeError{Code: exitNotReady, Err: errors.New("herdr unavailable")})
			}
			root, err := defaultStateRoot()
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "jump-back: history store error")
				return markReported(&ExitCodeError{Code: exitStoreError, Err: err})
			}
			paths, err := resolveSessionPaths(currentHerdrSocketPath(), root)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "jump-back: history not ready")
				return markReported(&ExitCodeError{Code: exitNotReady, Err: err})
			}
			return runJumpBack(cmd.Context(), jumpDriver, unixControlDialer{path: paths.ControlPath},
				paths.SessionKey, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runJumpBack performs the whole resolution with two independent fresh reads:
//
//  1. Ask the owner for readiness + MRU (a healthy answer proves a live owner).
//  2. Take a fresh snapshot; require it to agree with the owner about current.
//  3. Resolve the previous distinct LIVE workspace, skipping current.
//  4. Take a second fresh snapshot and require BOTH that the target is still
//     present AND that current is unchanged, then focus.
//
// No CAS exists on the public focus contract, so a residual window remains
// between step 4's validation and the focus call. That limit is documented
// rather than papered over.
func runJumpBack(ctx context.Context, driver jumpBackDriver, dialer controlDialer, sessionKey string, out, errOut io.Writer) error {
	state, err := queryOwnerState(ctx, dialer, sessionKey)
	if err != nil {
		fmt.Fprintln(errOut, "jump-back: history not ready")
		return markReported(&ExitCodeError{Code: exitNotReady, Err: err})
	}
	if state.ClassifiedError != "" {
		fmt.Fprintln(errOut, "jump-back: history store error")
		return markReported(&ExitCodeError{Code: exitStoreError, Err: errors.New(state.ClassifiedError)})
	}
	if !state.Ready {
		fmt.Fprintln(errOut, "jump-back: history not ready")
		return markReported(&ExitCodeError{Code: exitNotReady, Err: errors.New("collector reported unready history")})
	}

	// Step 2: the owner's view of "current" must agree with live Herdr state.
	resolveSnap, err := driver.Snapshot(ctx)
	if err != nil {
		fmt.Fprintln(errOut, "jump-back: target session no longer available")
		return markReported(&ExitCodeError{Code: exitSessionUnavailable, Err: err})
	}
	current := resolveSnap.FocusedWorkspaceID
	if current == "" || len(state.MRU) == 0 || state.MRU[0] != current {
		fmt.Fprintln(errOut, "jump-back: current workspace changed during resolve")
		return markReported(&ExitCodeError{Code: exitCurrentChanged, Err: errors.New("owner state disagrees with fresh snapshot")})
	}

	target, ok := resolvePreviousDistinct(state.MRU, current, liveWorkspaceIDs(resolveSnap))
	if !ok {
		fmt.Fprintln(errOut, "jump-back: no previous workspace")
		return markReported(&ExitCodeError{Code: exitNoHistory, Err: errors.New("no previous distinct live workspace")})
	}

	// Step 4: revalidate immediately before focusing.
	preFocus, err := driver.Snapshot(ctx)
	if err != nil {
		fmt.Fprintln(errOut, "jump-back: target session no longer available")
		return markReported(&ExitCodeError{Code: exitSessionUnavailable, Err: err})
	}
	live := liveWorkspaceIDs(preFocus)
	if _, present := live[target]; !present {
		fmt.Fprintln(errOut, "jump-back: target session no longer available")
		return markReported(&ExitCodeError{Code: exitSessionUnavailable, Err: errors.New("target absent from pre-focus snapshot")})
	}
	if preFocus.FocusedWorkspaceID != current {
		fmt.Fprintln(errOut, "jump-back: current workspace changed during resolve")
		return markReported(&ExitCodeError{Code: exitCurrentChanged, Err: errors.New("current changed before focus")})
	}

	// SourceHerdr with a workspace_id focuses the existing workspace; the
	// driver's create branch is never reached, so jump-back cannot create one.
	req := source.WorkspaceLaunchRequest{Candidate: source.Candidate{
		Source: config.SourceHerdr,
		Meta:   map[string]string{"workspace_id": target},
	}}
	if _, err := driver.FocusOrCreate(ctx, req); err != nil {
		// R7.S4 requires the focus failure to be surfaced; the design requires
		// user-facing errors to be sanitized classifications. Both hold: the
		// user is told focus failed for this target, while the driver's error
		// (which may embed an arbitrary command line or payload) stays in the
		// returned chain for programmatic observability instead of stderr.
		fmt.Fprintf(errOut, "jump-back: focus failed for workspace %s\n", target)
		return markReported(&ExitCodeError{Code: 1, Err: err})
	}

	fmt.Fprintln(out, target)
	return nil
}

// queryOwnerState performs one bounded control round trip. Any dial, write, or
// read failure is a not-ready signal: a hung or absent owner never yields a
// target, and stale SQLite content is never consulted by the CLI.
func queryOwnerState(ctx context.Context, dialer controlDialer, sessionKey string) (herdrwatch.StateResp, error) {
	queryCtx, cancel := context.WithTimeout(ctx, controlQueryTimeout)
	defer cancel()

	conn, err := dialer.Dial(queryCtx)
	if err != nil {
		return herdrwatch.StateResp{}, fmt.Errorf("dial control socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := herdrwatch.QueryState(queryCtx, conn, sessionKey)
	if err != nil {
		return herdrwatch.StateResp{}, fmt.Errorf("query owner state: %w", err)
	}
	return resp, nil
}

// resolvePreviousDistinct walks the newest-first MRU and returns the first
// entry that is not the current workspace, is still live, and is a safe
// identifier. Consecutive duplicates collapse naturally because every entry
// equal to current is skipped.
func resolvePreviousDistinct(mru []string, current string, live map[string]struct{}) (string, bool) {
	for _, id := range mru {
		if id == "" || id == current {
			continue
		}
		if !validWorkspaceID.MatchString(id) {
			continue
		}
		if _, ok := live[id]; !ok {
			continue
		}
		return id, true
	}
	return "", false
}

// liveWorkspaceIDs indexes a snapshot's workspaces for membership checks.
func liveWorkspaceIDs(snap source.Snapshot) map[string]struct{} {
	live := make(map[string]struct{}, len(snap.Workspaces))
	for _, ws := range snap.Workspaces {
		if ws.ID != "" {
			live[ws.ID] = struct{}{}
		}
	}
	return live
}
