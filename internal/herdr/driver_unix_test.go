//go:build darwin || linux

package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDriverSnapshot_BinaryEnvironmentRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "herdr-fifo")
	if err := makeFIFO(fifo); err != nil {
		t.Fatalf("make FIFO: %v", err)
	}

	runner := &fakeRunner{script: []fakeCall{{match: "configured-herdr api snapshot", out: []byte(`{"result":{"snapshot":{"workspaces":[],"tabs":[],"panes":[]}}}`)}}}
	driver := New("configured-herdr", WithRunner(runner), WithBinaryEnv(func(string) (string, bool) {
		return fifo, true
	}))
	if _, err := driver.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "configured-herdr api snapshot" {
		t.Fatalf("runner calls = %v, want configured fallback for FIFO", runner.calls)
	}
}

func makeFIFO(path string) error {
	if err := syscallMkfifo(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("created path is not a FIFO")
	}
	return nil
}

var syscallMkfifo = func(path string, mode uint32) error {
	return syscall.Mkfifo(path, mode)
}
