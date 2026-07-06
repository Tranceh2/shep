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

// TestPreview_BrokenCustomCommandHiddenFromOutput exercises the safe
// fallback path: a declared preview.commands entry that cannot run is
// silently omitted (requirement: hide command errors from normal preview
// output), leaving the built-in identity section intact with no warning
// leaking through.
func TestPreview_BrokenCustomCommandHiddenFromOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Preview.Default = []string{config.PreviewIdentity, "broken"}
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"broken": {Command: "shep-preview-command-does-not-exist-xyz {path}"},
	}
	out, _, err := runPreviewFor(t, cfg, dir)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Errorf("command failures must be hidden from normal preview output, got: %q", out)
	}
	if !strings.Contains(out, "path: ") {
		t.Errorf("expected built-in identity body despite the broken command, got: %q", out)
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

// TestWritePreview_StyledPlainTextStaysPlain and TestWritePreview_PlainNoANSI
// cover the CLI-layer color decision directly (isTerminalWriter itself is
// exercised separately), keeping the ANSI on/off contract unit-testable
// without a real pty. writePreview no longer flattens the body to a single
// accent color, so plain (never-colored) renderer text — like the built-in
// identity/git/workspace sections — must stay plain even when styled=true.
func TestWritePreview_StyledPlainTextStaysPlain(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writePreview(&out, preview.Result{Text: "hello"}, true)
	if strings.ContainsAny(out.String(), "\x1b") {
		t.Errorf("expected no ANSI added to plain text when styled=true, got: %q", out.String())
	}
	if strings.TrimSpace(out.String()) != "hello" {
		t.Errorf("expected exact text passthrough, got: %q", out.String())
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

// TestWritePreview_DefaultStripsRealRendererANSI (Television-safe default)
// simulates the kind of real ANSI a "dir"/"active_pane" section produces
// (lsd/eza --color=always output, a captured pane's true-color escapes) and
// confirms it is stripped to plain text when styled=false — the default, or
// --color without a terminal.
func TestWritePreview_DefaultStripsRealRendererANSI(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	// A true-color 24-bit escape, matching what lsd/eza --color=always or a
	// captured pane buffer would actually emit (not just basic 8-color codes).
	res := preview.Result{
		Text:    "\x1b[38;2;137;180;250mREADME.md\x1b[0m\n\x1b[1;34msrc\x1b[0m",
		Warning: "\x1b[33mWarning text\x1b[0m",
	}
	writePreview(&out, res, false)
	output := out.String()
	if strings.ContainsAny(output, "\x1b") {
		t.Errorf("expected real renderer ANSI to be stripped by default, got: %q", output)
	}
	if !strings.Contains(output, "README.md") || !strings.Contains(output, "src") {
		t.Errorf("expected clean text content preserved, got: %q", output)
	}
	if !strings.Contains(output, "warning: Warning text") {
		t.Errorf("expected warning to be sanitized, got: %q", output)
	}
}

// TestWritePreview_StyledPreservesRealRendererANSI is the core regression
// test for this fix: when styled=true (--color plus a real terminal), the
// renderer's own ANSI color (dir/active_pane's real colors) must survive
// byte-for-byte, matching what the interactive picker shows, instead of
// being stripped or flattened into a single accent color.
func TestWritePreview_StyledPreservesRealRendererANSI(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	rendererText := "\x1b[38;2;137;180;250mREADME.md\x1b[0m\n\x1b[1;34msrc\x1b[0m"
	res := preview.Result{
		Text:    rendererText,
		Warning: "\x1b[33mWarning text\x1b[0m",
	}
	writePreview(&out, res, true)
	output := out.String()
	if !strings.Contains(output, rendererText) {
		t.Errorf("expected renderer ANSI preserved byte-for-byte, got: %q", output)
	}
	// The warning line is always shep's own synthetic text: its raw escape
	// must still be stripped before shep applies its own warn styling.
	if strings.Contains(output, "\x1b[33m") {
		t.Errorf("expected raw warning ANSI to be stripped before restyling, got: %q", output)
	}
	if !strings.Contains(output, "Warning text") {
		t.Errorf("expected warning text content preserved, got: %q", output)
	}
}
