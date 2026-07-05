package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
)

// runPreviewFor executes `shep preview ...` against a fresh App and returns
// captured stdout/stderr. A nil cfg uses path-agnostic Defaults.
func runPreviewFor(t *testing.T, cfg *config.Config, args ...string) (string, string, error) {
	t.Helper()
	if cfg == nil {
		cfg = config.Defaults()
	}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	app.probes = config.Probes{}
	cmd := app.rootCmd()
	cmd.SetArgs(append([]string{"preview"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestPreview_PlainTextDefaultOutput (WP-4) confirms a valid path renders the
// built-in default preview as plain text with no ANSI escape codes.
func TestPreview_PlainTextDefaultOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, _, err := runPreviewFor(t, nil, dir)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if strings.ContainsAny(out, "\x1b") {
		t.Errorf("expected plain text (no ANSI escapes), got: %q", out)
	}
	if !strings.Contains(out, "path: ") {
		t.Errorf("expected default preview body with a path line, got: %q", out)
	}
}

// TestPreview_ColorFlagIgnoredWithoutTTY (WP-4) confirms --color has no
// effect when stdout is not a terminal (as in this test's bytes.Buffer),
// keeping the default contract stable for piped consumers like Television.
func TestPreview_ColorFlagIgnoredWithoutTTY(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, _, err := runPreviewFor(t, nil, "--color", dir)
	if err != nil {
		t.Fatalf("preview --color: %v", err)
	}
	if strings.ContainsAny(out, "\x1b") {
		t.Errorf("expected plain text when stdout is not a terminal, got: %q", out)
	}
}

// TestPreview_MissingPathReturnsCleanError (WP-4) asserts a missing path
// fails with a clean, user-facing stderr message and exit 1 — no raw
// stack/panic-shaped leakage.
func TestPreview_MissingPathReturnsCleanError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, errOut, err := runPreviewFor(t, nil, missing)
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(errOut, "does not exist") {
		t.Errorf("expected 'does not exist' message, got: %q", errOut)
	}
	if strings.Contains(errOut, "goroutine") || strings.Contains(errOut, "panic") {
		t.Errorf("stderr leaked internal detail: %q", errOut)
	}
}

// TestPreview_FileNotDirectoryReturnsError (WP-4) asserts a path that exists
// but is a regular file (not a workspace directory) fails cleanly.
func TestPreview_FileNotDirectoryReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runPreviewFor(t, nil, file)
	if err == nil {
		t.Fatal("expected error for a non-directory path")
	}
	if !strings.Contains(errOut, "not a directory") {
		t.Errorf("expected 'not a directory' message, got: %q", errOut)
	}
}

// TestPreview_EmptyPathReturnsError (WP-4) guards the empty-arg edge case.
func TestPreview_EmptyPathReturnsError(t *testing.T) {
	t.Parallel()
	_, errOut, err := runPreviewFor(t, nil, "")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if strings.TrimSpace(errOut) == "" {
		t.Error("expected a stderr message for empty path")
	}
}

// TestPreview_MissingArgReturnsError guards cobra's ExactArgs(1) contract.
func TestPreview_MissingArgReturnsError(t *testing.T) {
	t.Parallel()
	_, _, err := runPreviewFor(t, nil)
	if err == nil {
		t.Fatal("expected error when no path argument is given")
	}
}

// TestPreview_CommandFallbackSurfacesWarning (WP-3 via WP-4) exercises the
// safe-fallback path: a configured preview.command that cannot run degrades
// to the built-in preview plus a stderr-free, stdout warning line.
func TestPreview_CommandFallbackSurfacesWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Preview.Command = "shep-preview-command-does-not-exist-xyz {path}"
	out, _, err := runPreviewFor(t, cfg, dir)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(out, "warning:") {
		t.Errorf("expected a warning line for the fallback, got: %q", out)
	}
	if !strings.Contains(out, "path: ") {
		t.Errorf("expected built-in preview body in the fallback, got: %q", out)
	}
}

// TestPreviewCmd_ColorFlagRegistered confirms the --color flag is wired.
func TestPreviewCmd_ColorFlagRegistered(t *testing.T) {
	t.Parallel()
	app := New()
	cmd := app.previewCmd()
	if f := cmd.Flags().Lookup("color"); f == nil {
		t.Fatal("expected --color flag on the preview command")
	}
}

// TestWritePreview_StyledAppliesANSI and TestWritePreview_PlainNoANSI cover
// the CLI-layer color decision directly (isTerminalWriter itself is
// exercised separately), keeping the ANSI on/off contract unit-testable
// without a real pty.
func TestWritePreview_StyledAppliesANSI(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writePreview(&out, preview.Result{Text: "hello"}, true)
	if !strings.ContainsAny(out.String(), "\x1b") {
		t.Errorf("expected ANSI styling when styled=true, got: %q", out.String())
	}
}

func TestWritePreview_PlainNoANSI(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writePreview(&out, preview.Result{Text: "hello"}, false)
	if strings.ContainsAny(out.String(), "\x1b") {
		t.Errorf("expected plain text when styled=false, got: %q", out.String())
	}
	if strings.TrimSpace(out.String()) != "hello" {
		t.Errorf("expected exact text passthrough, got: %q", out.String())
	}
}

func TestWritePreview_WarningLineIncluded(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writePreview(&out, preview.Result{Text: "hello", Warning: "command failed"}, false)
	if !strings.Contains(out.String(), "warning: command failed") {
		t.Errorf("expected warning line, got: %q", out.String())
	}
}

// TestIsTerminalWriter_FalseForBuffer and TestIsTerminalWriter_FalseForRegularFile
// confirm the TTY gate degrades safely for non-terminal writers.
func TestIsTerminalWriter_FalseForBuffer(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if isTerminalWriter(&buf) {
		t.Error("expected false for a bytes.Buffer")
	}
}

func TestIsTerminalWriter_FalseForRegularFile(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminalWriter(f) {
		t.Error("expected false for a regular file")
	}
}

func TestWritePreview_SanitizesANSIInDefaultOutput(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	res := preview.Result{
		Text:    "\x1b[31mRed Text\x1b[0m and \x1b[1;34mBlue Text\x1b[0m",
		Warning: "\x1b[33mWarning text\x1b[0m",
	}
	writePreview(&out, res, false)
	output := out.String()
	if strings.ContainsAny(output, "\x1b") {
		t.Errorf("expected plain text (no ANSI escape codes), but found some: %q", output)
	}
	if !strings.Contains(output, "Red Text and Blue Text") {
		t.Errorf("expected clean text, got: %q", output)
	}
	if !strings.Contains(output, "warning: Warning text") {
		t.Errorf("expected warning to be sanitized, got: %q", output)
	}
}

func TestWritePreview_SanitizesANSIEvenInStyledOutput(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	res := preview.Result{
		Text:    "\x1b[31mRed Text\x1b[0m",
		Warning: "\x1b[33mWarning text\x1b[0m",
	}
	writePreview(&out, res, true)
	output := out.String()
	// Should contain ANSI sequence from shep-applied styling, but NOT the command-provided raw escape codes
	if strings.Contains(output, "[31m") || strings.Contains(output, "[33m") {
		t.Errorf("expected raw command-provided ANSI escapes ([31m or [33m) to be stripped, got: %q", output)
	}
	if !strings.Contains(output, "Red Text") {
		t.Errorf("expected 'Red Text' in output, got: %q", output)
	}
}
