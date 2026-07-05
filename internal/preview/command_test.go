package preview

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestParseCommand splits a shell-style command into argv, substitute {path} as
// one argument value, and honour quoting. No sh -c is ever used.
func TestParseCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cmd     string
		path    string
		want    []string
		wantErr bool
	}{
		{name: "simple", cmd: "echo hi", path: "/p", want: []string{"echo", "hi"}},
		{name: "path token replaced wholesale", cmd: "git -C {path} log", path: "/p/x", want: []string{"git", "-C", "/p/x", "log"}},
		{name: "path embedded in token", cmd: "ls {path}/sub", path: "/p", want: []string{"ls", "/p/sub"}},
		{name: "path with spaces stays one arg", cmd: "ls {path}", path: "/has space/x", want: []string{"ls", "/has space/x"}},
		{name: "double quoted arg stays single", cmd: `git commit -m "a b c"`, path: "/p", want: []string{"git", "commit", "-m", "a b c"}},
		{name: "single quoted arg stays single", cmd: `echo 'x y'`, path: "/p", want: []string{"echo", "x y"}},
		{name: "empty command errors", cmd: "   ", path: "/p", wantErr: true},
		{name: "unterminated quote errors", cmd: `echo "open`, path: "/p", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCommand(tc.cmd, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !sliceEq(got, tc.want) {
				t.Errorf("parse %q path=%q: got %v want %v", tc.cmd, tc.path, got, tc.want)
			}
		})
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCommandRunner_Run_Happy captures stdout from a real benign command.
func TestCommandRunner_Run_Happy(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	out, err := r.Run(context.Background(), []string{"printf", "hi\n"}, t.TempDir(), 50)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "hi" {
		t.Errorf("got %q want %q", out, "hi")
	}
}

// TestCommandRunner_Run_NonZeroExit treats a non-zero exit as failure.
func TestCommandRunner_Run_NonZeroExit(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	if _, err := r.Run(context.Background(), []string{"false"}, t.TempDir(), 50); err == nil {
		t.Fatal("expected error from non-zero exit, got nil")
	}
}

// TestCommandRunner_Run_StderrFails fails the command when stderr is non-empty
// (WP-3).
func TestCommandRunner_Run_StderrFails(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	_, err := r.Run(context.Background(), []string{"sh", "-c", "echo boom 1>&2"}, t.TempDir(), 50)
	if err == nil {
		t.Fatal("expected error when stderr is non-empty, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error must surface stderr content, got %q", err.Error())
	}
}

func TestCommandRunner_Run_HugeSingleLineStdoutIsBounded(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	out, err := r.Run(context.Background(), []string{"sh", "-c", "head -c 200000 /dev/zero | tr '\\0' x"}, t.TempDir(), 50)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) != maxCapturedOutputBytes {
		t.Fatalf("stdout len: got %d want %d", len(out), maxCapturedOutputBytes)
	}
	if strings.Contains(out, "\n") {
		t.Fatal("huge single-line stdout should remain one truncated line")
	}
}

func TestCommandRunner_Run_HugeStderrIsBounded(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	_, err := r.Run(context.Background(), []string{"sh", "-c", "head -c 200000 /dev/zero | tr '\\0' s 1>&2"}, t.TempDir(), 50)
	if err == nil {
		t.Fatal("expected error from stderr, got nil")
	}
	if len(err.Error()) > maxCapturedOutputBytes+128 {
		t.Fatalf("stderr error len: got %d, want bounded near %d", len(err.Error()), maxCapturedOutputBytes)
	}
	if !strings.Contains(err.Error(), strings.Repeat("s", 32)) {
		t.Fatalf("expected capped stderr sample in error, got %q", err.Error())
	}
}

// TestCommandRunner_Run_Timeout respects a short context deadline (WP-3).
func TestCommandRunner_Run_Timeout(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, []string{"sleep", "1"}, t.TempDir(), 50); err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestCommandRunner_Run_MaxLinesTruncation caps captured stdout (WP-3).
func TestCommandRunner_Run_MaxLinesTruncation(t *testing.T) {
	t.Parallel()

	r := commandRunner{}
	out, err := r.Run(context.Background(), []string{"printf", "1\n2\n3\n4\n"}, t.TempDir(), 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "1\n2" {
		t.Errorf("got %q want %q", out, "1\n2")
	}
}
