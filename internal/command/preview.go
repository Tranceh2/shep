package command

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/preview"
)

// previewColorHex and previewWarnColorHex are standalone colors inspired by
// the Catppuccin Mocha theme used in the TUI picker. They provide basic
// ANSI color hints for `shep preview --color` output, independent of the
// interactive picker's detailed layout styles.
const (
	previewColorHex     = "#89b4fa"
	previewWarnColorHex = "#f5c2e7"
)

// previewCmd builds `shep preview <path>` (WP-4): a stable, non-interactive
// way to render the same preview shown in the `shep open` picker's preview
// pane, so Television and other external tools can reuse it as a preview
// command. Output defaults to plain text (no ANSI escape codes); --color
// opts into Lip Gloss styling, applied only when stdout is a terminal.
func (a *App) previewCmd() *cobra.Command {
	var color bool
	cmd := &cobra.Command{
		Use:   "preview <path>",
		Short: "Render the workspace preview for a path",
		Long: `shep preview renders the same preview shown in the shep open picker's
preview pane for a single path, then exits. Output is plain text by default
(no ANSI escape codes) so Television and other external tools can safely
embed it as a preview command. Pass --color to opt into Lip Gloss styling;
it only takes effect when stdout is a terminal, so piped/captured output
always stays plain. An invalid or missing path reports a clean error on
stderr and exits non-zero.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPreview(cmd, args[0], color)
		},
	}
	cmd.Flags().BoolVar(&color, "color", false,
		"opt into Lip Gloss ANSI styling when stdout is a terminal (default: plain text)")
	return cmd
}

// runPreview resolves path to a candidate, renders it through the same
// preview.Renderer the picker uses, and writes the result to stdout. It is
// the pipeline so tests can call it directly against a fresh App.
func (a *App) runPreview(cmd *cobra.Command, path string, color bool) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	if err := validatePreviewPath(path); err != nil {
		fmt.Fprintf(errOut, "preview: %v\n", err)
		return errExitOne
	}

	cand, err := candidateFromPath(path)
	if err != nil {
		fmt.Fprintf(errOut, "preview: %v\n", err)
		return errExitOne
	}

	renderer := a.buildPreviewRenderer()
	res, renderErr := renderer.Render(cmd.Context(), cand, preview.RenderOptions{Color: color})
	if renderErr != nil {
		fmt.Fprintln(errOut, "preview: render failed")
		return errExitOne
	}

	writePreview(out, res, color && isTerminalWriter(out))
	return nil
}

// validatePreviewPath ensures path is non-empty and resolves to an existing
// directory before candidateFromPath builds a candidate for it (WP-4: a
// missing or invalid path must fail cleanly instead of rendering an empty or
// misleading preview).
func validatePreviewPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("path does not exist: %s", abs)
		}
		return fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory: %s", abs)
	}
	return nil
}

// ansiRegex matches ANSI/control sequences (CSI, OSC, etc.).
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// stripANSI removes ANSI and control sequences from a string.
func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// writePreview writes the rendered preview text and any transient warning
// (WP-3's safe command fallback) to out, applying Lip Gloss styling only when
// styled is true (the caller has already gated that on --color plus a
// terminal check). styled forces a color-capable renderer scoped to out
// rather than re-detecting out's terminal capabilities, since the caller
// already made that decision (isTerminalWriter) before committing to styled
// output.
func writePreview(out io.Writer, res preview.Result, styled bool) {
	text := stripANSI(res.Text)
	warn := ""
	if res.Warning != "" {
		warn = "warning: " + stripANSI(res.Warning)
	}
	if styled {
		r := lipgloss.NewRenderer(out)
		r.SetColorProfile(termenv.TrueColor)
		text = r.NewStyle().Foreground(lipgloss.Color(previewColorHex)).Render(text)
		if warn != "" {
			warn = r.NewStyle().Foreground(lipgloss.Color(previewWarnColorHex)).Italic(true).Render(warn)
		}
	}
	fmt.Fprintln(out, text)
	if warn != "" {
		fmt.Fprintln(out, warn)
	}
}

// isTerminalWriter reports whether w is an *os.File connected to a terminal.
// Forcing ANSI onto a pipe would break Television/fzf consumers that expect
// the default plain-text contract (WP-4), so --color only takes effect here.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}
