package command

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
)

// doctorCmd builds `shep doctor`, a read-only diagnostic that reports
// configured [[workspaces]] paths that do not exist on disk, plus the state of
// the published PATH name. It never creates directories, never links or
// unlinks, and never fails the config load; a problem is a warning, not a hard
// error, so doctor always exits 0.
func (a *App) doctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check workspace paths, the picker theme and the published PATH name",
		Long: `shep doctor checks every [[workspaces]] entry (including group workspaces)
and reports any whose path does not exist on disk. It never creates
directories and never fails the config; missing paths are reported as
warnings so you can fix your config.toml before they surface as a confusing
"path does not exist" error from shep open. Templates and colors are checked
when the configuration loads, so a doctor run that reaches this report has a
valid configuration.

It reports the color theme the picker uses and where it came from
(NO_COLOR, SHEP_THEME, [tui].theme, or inherited from Herdr), with Herdr's
own diagnostics for its theme settings when the theme inherits from it.

It also reports what the published PATH name currently resolves to, so you can
tell which install a bare "shep" reaches. doctor only observes: it never links,
unlinks, or edits your shell profile.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runDoctor(cmd)
		},
	}
	return cmd
}

// runDoctor is the pipeline so tests can call it directly against a fresh App.
func (a *App) runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	cfg := a.Config()
	// An empty workspace list is not a reason to skip the rest of the report:
	// the theme and the published PATH name are worth knowing regardless of
	// how many workspaces are configured, and a fresh install with no config
	// is exactly when an operator asks where a bare `shep` points.
	a.reportWorkspaces(out, cfg)
	a.reportTheme(out, cfg)
	a.reportPublishedName(out)
	return nil
}

// reportWorkspaces checks that every [[workspaces]] path exists.
func (a *App) reportWorkspaces(out io.Writer, cfg *config.Config) {
	if len(cfg.Workspaces) == 0 {
		fmt.Fprintln(out, "no [[workspaces]] entries configured")
		return
	}
	missing := 0
	for _, ws := range cfg.Workspaces {
		// Fall back to the raw path on an unresolvable HOME so os.Stat reports
		// the bad path instead of crashing on expansion.
		path := ws.Path
		if expanded, err := pathutil.ExpandTilde(ws.Path); err == nil {
			path = expanded
		}
		if _, err := os.Stat(path); err != nil {
			fmt.Fprintf(out, "MISSING %s: %s (configured path does not exist)\n", ws.Name, path)
			missing++
			continue
		}
		fmt.Fprintf(out, "OK      %s: %s\n", ws.Name, path)
	}
	if missing > 0 {
		fmt.Fprintf(out, "\n%d of %d configured workspace path(s) missing\n", missing, len(cfg.Workspaces))
	}
}

// reportTheme prints the theme the picker renders with and where it came
// from (theme.Source), the Herdr configuration it inherits from, and every
// note: an ignored SHEP_THEME, a missing Herdr configuration, Herdr's own
// diagnostics (unknown theme names, colors Herdr would draw as cyan).
func (a *App) reportTheme(out io.Writer, cfg *config.Config) {
	t, err := a.selectTheme(cfg)
	if err != nil {
		fmt.Fprintf(out, "\nTheme: ERROR %v\n", err)
		return
	}
	fmt.Fprintf(out, "\nTheme: %s\n", t.Name)
	fmt.Fprintf(out, "  source: %s\n", t.Source)
	if t.Source.HerdrPath != "" {
		fmt.Fprintf(out, "  herdr config: %s\n", t.Source.HerdrPath)
	}
	for _, note := range t.Source.Notes {
		fmt.Fprintf(out, "  note: %s\n", note)
	}
}

// reportPublishedName describes what the published PATH name currently points
// at. It answers the one question `shep link` cannot: when several installs
// exist, which one does a bare `shep` actually reach?
//
// It is strictly read-only. It probes without following the final symlink, for
// the same reason link does — the question is what the NAME is, and following
// it would report the binary while a dangling link would read as absent.
// Anything it cannot resolve is reported as unknown rather than guessed, and it
// never links, unlinks, or edits a shell profile.
func (a *App) reportPublishedName(out io.Writer) {
	env := a.getLinkEnv()
	home, err := a.getUserHomeDir()()
	if err != nil {
		fmt.Fprintf(out, "\nPATH name: unknown (cannot resolve home directory: %v)\n", err)
		return
	}

	dir := linkDir(home, env)
	dest := linkPath(home, env)
	fmt.Fprintf(out, "\nPATH name: %s\n", dest)

	// A failure to resolve our own path must not be reported as a mismatch:
	// "not this install" and "we do not know which install we are" are
	// different answers, and only the first is a finding.
	own, ownErr := a.resolveOwn()

	switch probe := a.getLinkFS().Probe(dest); probe.Kind {
	case probeAbsent:
		fmt.Fprintf(out, "  not linked (run `shep link` to publish it)\n")
	case probeSymlink:
		switch {
		case ownErr != nil:
			fmt.Fprintf(out, "  links to %s (cannot compare with this install: %v)\n", probe.Target, ownErr)
		case probe.Target == own:
			fmt.Fprintf(out, "  links to this install (%s)\n", probe.Target)
		case isShepBinaryPath(probe.Target):
			fmt.Fprintf(out, "  links to a DIFFERENT shep install: %s\n", probe.Target)
			fmt.Fprintf(out, "  a bare `shep` runs that one, not %s; run `shep link` here to take the name over\n", own)
		default:
			fmt.Fprintf(out, "  occupied by a symlink to %s, which shep never published\n", probe.Target)
		}
	default:
		fmt.Fprintf(out, "  occupied by %s, which shep will not replace\n", probe.What)
	}

	if !onPath(dir, env("PATH")) {
		fmt.Fprintf(out, "  note: %s is not on your PATH; a bare `%s` will not be found\n", dir, linkedName)
	}
}
