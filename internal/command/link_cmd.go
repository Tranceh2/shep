package command

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// linkFS is the filesystem edge link/unlink need. Production uses realLinkFS;
// tests substitute an in-memory double so no test ever writes into the
// developer's real ~/.local/bin.
type linkFS interface {
	Probe(path string) linkProbe
	MkdirAll(dir string) error
	Symlink(target, at string) error
	Remove(at string) error
	Exists(path string) bool
}

type realLinkFS struct{}

func (realLinkFS) Probe(path string) linkProbe {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
			return linkProbe{Kind: probeAbsent}
		}
		return linkProbe{Kind: probeOther, What: "an unreadable entry"}
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		rawTarget, err := os.Readlink(path)
		if err != nil {
			return linkProbe{Kind: probeOther, What: "an unreadable symlink"}
		}
		return linkProbe{
			Kind:   probeSymlink,
			Target: resolveLinkTarget(path, rawTarget),
		}
	}
	if fi.IsDir() {
		return linkProbe{Kind: probeOther, What: "a directory"}
	}
	return linkProbe{Kind: probeOther, What: "a regular file"}
}

func (realLinkFS) MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

func (realLinkFS) Symlink(target, at string) error {
	return os.Symlink(target, at)
}

func (realLinkFS) Remove(at string) error {
	return os.Remove(at)
}

func (realLinkFS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (a *App) getLinkFS() linkFS {
	if a.linkFS != nil {
		return a.linkFS
	}
	return realLinkFS{}
}

func (a *App) getUserHomeDir() func() (string, error) {
	if a.userHomeDir != nil {
		return a.userHomeDir
	}
	return os.UserHomeDir
}

func (a *App) getLinkEnv() linkEnv {
	if a.linkEnv != nil {
		return a.linkEnv
	}
	return os.Getenv
}

func (a *App) resolveOwn() (string, error) {
	exeFunc := a.executable
	if exeFunc == nil {
		exeFunc = os.Executable
	}
	exe, err := exeFunc()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	absExe, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path: %w", err)
	}
	evalFunc := a.evalSymlinks
	if evalFunc == nil {
		evalFunc = filepath.EvalSymlinks
	}
	canonical, err := evalFunc(absExe)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for executable path %q: %w", absExe, err)
	}
	return canonical, nil
}

// linkCmd builds `shep link` which publishes a symlink to this shep binary
// in the user's binary directory (~/.local/bin by default).
func (a *App) linkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link",
		Short: "Symlink shep into your PATH",
		Long: `shep link publishes a symlink to this shep binary in your user binary directory
(~/.local/bin by default, or $SHEP_LINK_DIR / $XDG_BIN_HOME when set).

Because it publishes a symlink rather than a copy, rebuilds and plugin updates
take effect immediately without re-linking.

If the destination is already a symlink pointing to another Shep install, link
replaces it. If the destination is occupied by a foreign symlink, a regular file,
or a directory, link refuses to touch it.

shep link never modifies your shell profile. If the target directory is not on
your PATH, it prints a note with instructions.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"shep/skip-preload": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			own, err := a.resolveOwn()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return errExitOne
			}

			fsys := a.getLinkFS()
			if !fsys.Exists(own) {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: binary not found at %s\n", own)
				return errExitOne
			}

			homeFunc := a.getUserHomeDir()
			home, err := homeFunc()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: resolve home directory: %v\n", err)
				return errExitOne
			}

			env := a.getLinkEnv()
			dir := linkDir(home, env)
			dest := linkPath(home, env)

			if own == dest {
				fmt.Fprintf(cmd.ErrOrStderr(), "cannot link %s to itself (already running from the destination path)\n", dest)
				return errExitOne
			}

			probe := fsys.Probe(dest)
			verdict := classifyLink(probe, own)

			out := cmd.OutOrStdout()
			switch verdict.Action {
			case linkRefuse:
				fmt.Fprintf(cmd.ErrOrStderr(), "refusing to link %s: destination is %s\nmove it aside and re-run shep link\n", dest, verdict.Reason)
				return errExitOne
			case linkKeep:
				fmt.Fprintf(out, "%s already links to %s\n", dest, own)
			case linkCreate:
				if err := fsys.MkdirAll(dir); err != nil {
					return fmt.Errorf("create directory %s: %w", dir, err)
				}
				if err := fsys.Symlink(own, dest); err != nil {
					return fmt.Errorf("create symlink %s: %w", dest, err)
				}
				fmt.Fprintf(out, "linked %s -> %s\n", dest, own)
			case linkReplace:
				if err := fsys.MkdirAll(dir); err != nil {
					return fmt.Errorf("create directory %s: %w", dir, err)
				}
				if err := fsys.Remove(dest); err != nil {
					return fmt.Errorf("remove previous symlink %s: %w", dest, err)
				}
				if err := fsys.Symlink(own, dest); err != nil {
					return fmt.Errorf("create symlink %s: %w", dest, err)
				}
				fmt.Fprintf(out, "replaced %s (was -> %s)\nlinked %s -> %s\n", dest, verdict.Previous, dest, own)
			}

			if !onPath(dir, env("PATH")) {
				fmt.Fprintf(out, "note: %s is not on your PATH; add it to your shell profile to use %s\n", dir, linkedName)
			}
			return nil
		},
	}
}

// unlinkCmd builds `shep unlink` which removes the symlink published by `shep link`.
func (a *App) unlinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlink",
		Short: "Remove the shep symlink from your PATH",
		Long: `shep unlink removes the symlink published by shep link.

Only a symlink pointing to this specific Shep install is removed. If the name is
unlinked, unlink succeeds idempotently. If the destination points to another Shep
install or a non-Shep file, unlink refuses to touch it.

Removing the symlink leaves the underlying Shep install untouched.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"shep/skip-preload": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			own, err := a.resolveOwn()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return errExitOne
			}

			homeFunc := a.getUserHomeDir()
			home, err := homeFunc()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: resolve home directory: %v\n", err)
				return errExitOne
			}

			env := a.getLinkEnv()
			dest := linkPath(home, env)

			fsys := a.getLinkFS()
			probe := fsys.Probe(dest)
			verdict := classifyUnlink(probe, own)

			out := cmd.OutOrStdout()
			switch verdict.Action {
			case unlinkAbsent:
				fmt.Fprintf(out, "%s is not linked\n", dest)
				return nil
			case unlinkRefuse:
				fmt.Fprintf(cmd.ErrOrStderr(), "refusing to unlink %s: %s\nrun shep unlink from the owning install or remove it manually\n", dest, verdict.Reason)
				return errExitOne
			case unlinkRemove:
				if err := fsys.Remove(dest); err != nil {
					return fmt.Errorf("remove symlink %s: %w", dest, err)
				}
				fmt.Fprintf(out, "removed %s (the shep install at %s is untouched)\n", dest, own)
				return nil
			}
			return nil
		},
	}
}
