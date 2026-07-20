package preview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/rowformat"
)

// TestParseCommand tokenizes before rendering each template action. Rendered
// values stay inside their already-isolated argv elements; no sh -c is used.
func TestParseCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cmd     string
		ctx     rowformat.Context
		want    []string
		wantErr bool
	}{
		{name: "simple", cmd: "echo hi", ctx: rowformat.Context{Path: "/p"}, want: []string{"echo", "hi"}},
		{name: "template path token renders", cmd: "git -C {{.Path}} log", ctx: rowformat.Context{Path: "/p/x"}, want: []string{"git", "-C", "/p/x", "log"}},
		{name: "template path embedded in token renders", cmd: "ls {{.Path}}/sub", ctx: rowformat.Context{Path: "/p"}, want: []string{"ls", "/p/sub"}},
		{name: "path and label shell metacharacters remain isolated", cmd: "printf {{.Path}} {{.Label}}", ctx: rowformat.Context{Path: "/has space/x;rm -rf ~", Label: "release candidate;$(touch nope)"}, want: []string{"printf", "/has space/x;rm -rf ~", "release candidate;$(touch nope)"}},
		{name: "template syntax in path remains inert data", cmd: "echo {{.Path}}", ctx: rowformat.Context{Path: "{{.Label}}"}, want: []string{"echo", "{{.Label}}"}},
		{name: "double quoted arg stays single", cmd: `git commit -m "a b c"`, ctx: rowformat.Context{Path: "/p"}, want: []string{"git", "commit", "-m", "a b c"}},
		{name: "single quoted arg stays single", cmd: `echo 'x y'`, ctx: rowformat.Context{Path: "/p"}, want: []string{"echo", "x y"}},
		{name: "unquoted template action with whitespace errors", cmd: "echo {{ .Path }}", ctx: rowformat.Context{Path: "/has space/x"}, wantErr: true},
		{name: "quoted template action with whitespace succeeds", cmd: `echo "{{ .Path }}"`, ctx: rowformat.Context{Path: "/has space/x"}, want: []string{"echo", "/has space/x"}},
		{name: "empty command errors", cmd: "   ", ctx: rowformat.Context{Path: "/p"}, wantErr: true},
		{name: "unterminated quote errors", cmd: `echo "open`, ctx: rowformat.Context{Path: "/p"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCommand(tc.cmd, tc.ctx)
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
				t.Errorf("parse %q context=%+v: got %v want %v", tc.cmd, tc.ctx, got, tc.want)
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
