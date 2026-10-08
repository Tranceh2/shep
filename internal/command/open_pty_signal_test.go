//go:build darwin || linux

package command

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// R4-001 was refuted by exploration #2243: Herdr's raw-mode terminal sends
// Ctrl+C as byte 0x03, while an explicit OS SIGINT remains independently
// observable. The first nested topology deadlocked on Darwin (#2251/#2260);
// this suite keeps one self-reexec and one ctty owner.
const (
	ptyRoleEnv = "SHEP_PTY_ROLE"
	ptyIPCEnv  = "SHEP_PTY_IPC"
)

var _ sessionAttachFunc = runSessionAttach

func TestMain(m *testing.M) {
	switch os.Getenv(ptyRoleEnv) {
	case "attach_child_raw":
		os.Exit(runAttachChildRaw())
	case "attach_child_canonical":
		os.Exit(runAttachChildCanonical())
	case "attach_child_sigint":
		os.Exit(runAttachChildSigint())
	default:
		// No test may reach a live Herdr: a real driver built without an
		// injected one talks to HERDR_SOCKET_PATH, which is set whenever the
		// suite runs inside a Herdr pane. Nor may one write the user's state
		// or cache (the search history, saved source results).
		_ = os.Unsetenv("HERDR_SOCKET_PATH")
		scratch, err := os.MkdirTemp("", "shep-xdg")
		if err != nil {
			panic(err)
		}
		_ = os.Setenv("XDG_STATE_HOME", filepath.Join(scratch, "state"))
		_ = os.Setenv("XDG_CACHE_HOME", filepath.Join(scratch, "cache"))
		code := m.Run()
		_ = os.RemoveAll(scratch)
		os.Exit(code)
	}
}

func TestSessionAttach_RawCtrlC_IsInputNotSignal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open PTY: %v", err)
	}
	ipcPath, ipcReader := newPTYIPC(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	restoreStdio := replaceProcessStdio(slave, slave, slave)
	runErr := make(chan error, 1)
	done := make(chan struct{})
	env := append(os.Environ(), ptyRoleEnv+"=attach_child_raw", ptyIPCEnv+"="+ipcPath)
	go func() {
		defer close(done)
		runErr <- runSessionAttach(ctx, os.Args[0], "attach", env)
	}()
	defer cleanupAttach(t, cancel, done, restoreStdio, master, slave, ipcReader)

	protocol := bufio.NewReader(ipcReader)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	ready, err := readProtocolLine(ctx, ipcReader, protocol)
	if err != nil {
		t.Fatalf("read raw child readiness: %v", err)
	}
	pid, err := parsePIDLine(ready, "READY:")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := master.Write([]byte{0x03}); err != nil {
		t.Fatalf("write Ctrl+C byte to PTY master: %v", err)
	}
	got, err := readProtocolLine(ctx, ipcReader, protocol)
	if err != nil {
		t.Fatalf("read raw child byte report: %v", err)
	}
	if got != "GOT:03" {
		t.Fatalf("raw child report = %q, want GOT:03", got)
	}

	if err := waitForAttach(ctx, runErr); err != nil {
		t.Fatalf("runSessionAttach returned error after raw input: %v", err)
	}
	select {
	case sig := <-sigCh:
		t.Fatalf("parent received unexpected signal after raw 0x03: %v", sig)
	default:
	}
	assertReaped(t, pid)
}

func TestSessionAttach_CanonicalCtrlC_IsSignalNegativeControl(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open PTY: %v", err)
	}
	ipcPath, ipcReader := newPTYIPC(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	restoreStdio := replaceProcessStdio(slave, slave, slave)
	runErr := make(chan error, 1)
	done := make(chan struct{})
	env := append(os.Environ(), ptyRoleEnv+"=attach_child_canonical", ptyIPCEnv+"="+ipcPath)
	go func() {
		defer close(done)
		runErr <- runSessionAttach(ctx, os.Args[0], "attach", env)
	}()
	defer cleanupAttach(t, cancel, done, restoreStdio, master, slave, ipcReader)

	protocol := bufio.NewReader(ipcReader)
	ready, err := readProtocolLine(ctx, ipcReader, protocol)
	if err != nil {
		t.Fatalf("read canonical child readiness: %v", err)
	}
	pid, err := parsePIDLine(ready, "READY:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte{0x03}); err != nil {
		t.Fatalf("write Ctrl+C byte to canonical PTY master: %v", err)
	}

	err = waitForAttach(ctx, runErr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("canonical child error = %v, want SIGINT exit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("canonical child exit status type = %T, want syscall.WaitStatus", exitErr.Sys())
	}
	if got := status.Signal(); got != syscall.SIGINT {
		t.Fatalf("canonical child signal = %v, want %v", got, syscall.SIGINT)
	}
	assertReaped(t, pid)
}

func TestSessionAttach_ExplicitSIGINT_CleanExit(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create child stdin pipe: %v", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		t.Fatalf("open null stdio: %v", err)
	}
	ipcPath, ipcReader := newPTYIPC(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	restoreStdio := replaceProcessStdio(stdinR, devNull, devNull)
	runErr := make(chan error, 1)
	done := make(chan struct{})
	env := append(os.Environ(), ptyRoleEnv+"=attach_child_sigint", ptyIPCEnv+"="+ipcPath)
	go func() {
		defer close(done)
		runErr <- runSessionAttach(ctx, os.Args[0], "attach", env)
	}()
	defer cleanupAttach(t, cancel, done, restoreStdio, stdinR, stdinW, devNull, ipcReader)

	protocol := bufio.NewReader(ipcReader)
	pidLine, err := readProtocolLine(ctx, ipcReader, protocol)
	if err != nil {
		t.Fatalf("read SIGINT child PID: %v", err)
	}
	pid, err := parsePIDLine(pidLine, "PID:")
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to child %d: %v", pid, err)
	}

	err = waitForAttach(ctx, runErr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runSessionAttach error = %v, want SIGINT exit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("exit status type = %T, want syscall.WaitStatus", exitErr.Sys())
	}
	if got := status.Signal(); got != syscall.SIGINT {
		t.Fatalf("child signal = %v, status=%#v exited=%t exitcode=%d signaled=%t, want %v", got, status, status.Exited(), status.ExitStatus(), status.Signaled(), syscall.SIGINT)
	}
	assertReaped(t, pid)
}

func newPTYIPC(t *testing.T) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attach.fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create PTY IPC FIFO: %v", err)
	}
	reader, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open PTY IPC FIFO: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path, reader
}

func readProtocolLine(ctx context.Context, reader *os.File, protocol *bufio.Reader) (string, error) {
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()
	line, err := protocol.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func parsePIDLine(line, prefix string) (int, error) {
	if !strings.HasPrefix(line, prefix) {
		return 0, fmt.Errorf("protocol line = %q, want %s<pid>", line, prefix)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(line, prefix))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("protocol PID = %q: %w", line, err)
	}
	return pid, nil
}

func waitForAttach(ctx context.Context, runErr <-chan error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-runErr:
		return err
	}
}

func assertReaped(t *testing.T, pid int) {
	t.Helper()
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("kill(%d, 0) = %v, want ESRCH after Wait reaped child", pid, err)
	}
}

func replaceProcessStdio(stdin, stdout, stderr *os.File) func() {
	oldStdin, oldStdout, oldStderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr
	return func() {
		os.Stdin, os.Stdout, os.Stderr = oldStdin, oldStdout, oldStderr
	}
}

func cleanupAttach(t *testing.T, cancel context.CancelFunc, done <-chan struct{}, restoreStdio func(), files ...io.Closer) {
	t.Helper()
	cancel()
	cleanupCtx, cleanupCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cleanupCancel()
	select {
	case <-done:
		restoreStdio()
		for _, file := range files {
			if file != nil {
				_ = file.Close()
			}
		}
	case <-cleanupCtx.Done():
		t.Errorf("timed out waiting for attach process cleanup: %v", cleanupCtx.Err())
	}
}

func runAttachChildRaw() int {
	if _, err := unix.Setsid(); err != nil {
		return reportChildError("setsid", err)
	}
	if err := unix.IoctlSetInt(int(os.Stdin.Fd()), unix.TIOCSCTTY, 0); err != nil {
		return reportChildError("TIOCSCTTY", err)
	}
	termios, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), getTermiosReq)
	if err != nil {
		return reportChildError("get termios", err)
	}
	termios.Lflag &^= unix.ISIG | unix.ICANON | unix.ECHO
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(int(os.Stdin.Fd()), setTermiosReq, termios); err != nil {
		return reportChildError("set raw termios", err)
	}
	conn, err := openPTYIPC()
	if err != nil {
		return 1
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "READY:%d\n", os.Getpid()); err != nil {
		return 1
	}
	var input [1]byte
	if _, err := os.Stdin.Read(input[:]); err != nil {
		return 1
	}
	if _, err := fmt.Fprintf(conn, "GOT:%02x\n", input[0]); err != nil {
		return 1
	}
	return 0
}

func runAttachChildCanonical() int {
	if _, err := unix.Setsid(); err != nil {
		return reportChildError("setsid", err)
	}
	if err := unix.IoctlSetInt(int(os.Stdin.Fd()), unix.TIOCSCTTY, 0); err != nil {
		return reportChildError("TIOCSCTTY", err)
	}
	termios, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), getTermiosReq)
	if err != nil {
		return reportChildError("get termios", err)
	}
	termios.Lflag |= unix.ISIG | unix.ICANON
	termios.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(os.Stdin.Fd()), setTermiosReq, termios); err != nil {
		return reportChildError("set canonical termios", err)
	}
	conn, err := openPTYIPC()
	if err != nil {
		return 1
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "READY:%d\n", os.Getpid()); err != nil {
		return 1
	}
	var input [1]byte
	_, _ = os.Stdin.Read(input[:])
	return 1
}

func runAttachChildSigint() int {
	conn, err := openPTYIPC()
	if err != nil {
		return 1
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "PID:%d\n", os.Getpid()); err != nil {
		return 1
	}
	var input [1]byte
	if _, err := os.Stdin.Read(input[:]); err != nil {
		return 1
	}
	return 0
}

func openPTYIPC() (*os.File, error) {
	path := os.Getenv(ptyIPCEnv)
	if path == "" {
		return nil, errors.New("missing PTY IPC path")
	}
	return os.OpenFile(path, os.O_WRONLY, 0)
}

func reportChildError(operation string, err error) int {
	conn, openErr := openPTYIPC()
	if openErr == nil {
		_, _ = fmt.Fprintf(conn, "ERROR:%s: %v\n", operation, err)
		_ = conn.Close()
	}
	return 1
}
