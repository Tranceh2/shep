package command

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/tranceh2/shep/internal/preview"
)

// previewCmd builds `shep preview <path>`: a stable, non-interactive
// way to render the same preview shown in the `shep open` picker's preview
// pane, so Television and other external tools can reuse it as a preview
// command. Output defaults to plain text (no ANSI escape codes) so it is
// safe for any consumer; --color opts into passing through any real ANSI
// color the renderer already produced (e.g. lsd/eza coloring for "dir", a
// captured pane's colors for "active_pane"). --color is FORCED when passed —
// matching `lsd --color=always`/`eza --color=always`/`bat --color=always` —
// regardless of whether stdout is a terminal, because the whole point of the
// flag is the embedding use case: Television (or any tool piping this as a
// subprocess) sees a pipe, never a tty, so a tty gate would defeat it. The
// default (no --color) always strips ANSI.
func (a *App) previewCmd() *cobra.Command {
	var color bool
	cmd := &cobra.Command{
		Use:   "preview <path>",
		Short: "Render the workspace preview for a path",
		Long: `shep preview renders the same preview shown in the shep open picker's
preview pane for a single path, then exits. Output is plain text by default
(no ANSI escape codes) so Television and other external tools can safely
embed it as a preview command. Pass --color to keep any real ANSI color the
renderer already produced (e.g. a directory listing's own colors, or a
captured pane's real terminal colors), so the output matches the picker.
Unlike a tty auto-detect, --color takes effect even when stdout is a pipe —
matching ` + "`lsd --color=always`" + `/` + "`eza --color=always`" + `/` + "`bat --color=always`" + ` — because the
common caller is Television running ` + "`shep preview`" + ` as a subprocess (whose stdout
is never a terminal). An invalid or missing path reports a clean error on
stderr and exits non-zero.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPreview(cmd, args[0], color)
		},
	}
	cmd.Flags().BoolVar(&color, "color", false,
		"force real ANSI color from the renderer through to stdout (like --color=always); default is plain text")
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
	res, renderErr := renderer.Render(cmd.Context(), cand)
	if renderErr != nil {
		fmt.Fprintln(errOut, "preview: render failed")
		return errExitOne
	}

	// color is honored verbatim: when --color is passed the renderer's real
	// ANSI is passed through unmodified regardless of whether stdout is a
	// terminal (Television pipes this command, so a tty gate would strip the
	// very color the flag exists to surface). The default (color == false)
	// always strips ANSI, keeping the plain-text contract for any consumer.
	writePreview(out, res, color)
	return nil
}

// validatePreviewPath ensures path is non-empty and resolves to an existing
// directory before candidateFromPath builds a candidate for it: a missing or
// invalid path fails cleanly instead of rendering an empty or misleading
// preview.
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

// stripANSI removes ANSI and control sequences (CSI, OSC, DCS, and the rest
// of the C1/CTL escape set) from a string. Delegates to
// charmbracelet/x/ansi.Strip — the same package truncateToWidth (internal/tui)
// already trusts for ANSI-aware truncation — rather than a hand-rolled regex,
// so the full escape set is covered (a regex only matching CSI would leave
// OSC/DCS sequences in Television-facing default output).
func stripANSI(s string) string {
	return ansi.Strip(s)
}

// writePreview writes the rendered preview text to out. Precedence between
// the two output modes:
//
//   - styled == false (the default, i.e. --color not passed): res.Text is
//     always run through stripANSI. This keeps the Television-safe contract —
//     plain text, no escape codes — so any piped or captured output never
//     surprises a script consumer.
//   - styled == true (--color passed): res.Text is written through
//     UNMODIFIED, regardless of whether out is a terminal. The renderer may
//     have already produced real ANSI color for some sections (e.g. lsd/eza's
//     own coloring for "dir", or a captured pane's real terminal colors for
//     "active_pane"); passing it through verbatim is what makes `shep
//     preview --color` actually match what the interactive picker shows — and
//     what lets Television's preview panel render real color when it runs
//     this command as a subprocess (stdout is a pipe, never a tty). Sections
//     that never carried color ("identity", "git", "workspace") simply render
//     as plain text here too, since they had none to begin with — this
//     function never synthesizes color, it only decides whether to keep or
//     strip what the renderer already emitted.
func writePreview(out io.Writer, res preview.Result, styled bool) {
	text := res.Text
	if !styled {
		text = stripANSI(text)
	}
	fmt.Fprintln(out, text)
}
