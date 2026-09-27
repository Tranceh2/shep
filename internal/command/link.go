package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// `shep link` / `shep unlink` publish this install's binary under a name on the
// operator's PATH, and take that name back down.
//
// ── WHY SHEP OWNS THIS AT ALL ────────────────────────────────────────────────
// Herdr runs a plugin's declared commands as `herdr plugin action invoke <id>
// --plugin <plugin_id>`, with the exact argv the manifest declares. It has no
// `herdr <plugin> ...` dispatch, no plugin exec/run verb, no global plugin bin
// directory, and it never edits PATH. An action therefore cannot carry arguments,
// which leaves `shep open <query>`, `shep list --format tsv` (the Television
// cable's source), and every other argument-taking verb unreachable through
// Herdr. A bare `shep` has to come from somewhere, and the only honest somewhere
// is Shep itself — published deliberately, by an explicit verb, never as a side
// effect of installing or building.
//
// ── A POINTER, NEVER A COPY ──────────────────────────────────────────────────
// What lands in the link directory is a SYMLINK to this install's binary, not a
// copy. A copy would be a second artifact to keep in step, and it would go stale
// the moment the install is rebuilt or Herdr replaces its managed checkout on
// reinstall. The symlink target is also DISCOVERED at run time via os.Executable
// rather than spelled out: Herdr's managed plugin directory carries a hash
// suffix (`herdr.collie-1edf0e1e987e` in a real installed plugin), so any
// hardcoded target is wrong on the next install.
//
// ── ONLY TEAR DOWN WHAT MATCHES YOUR OWN RECORD ──────────────────────────────
// The destination is judged by its SHAPE, never by trusting that we put it
// there. A symlink whose target names some install's `bin/shep` is a name Shep
// published, so `link` may replace it — loudly, naming what it pointed at.
// Anything else (a regular file, a directory, a symlink into another tool) is
// refused untouched. `unlink` is stricter: it removes the name only when it
// points at THIS install's binary, because a link to another install belongs to
// that install.
//
// Nothing here edits a shell profile. Publishing a name is one act; rewriting
// how the operator's shell is configured is a different one, and a verb that
// quietly appended to .zshrc would be doing the second while claiming the first.

// linkedName is the basename published on PATH. It is also the suffix
// isShepBinaryPath matches, so both sides of the decision agree by construction.
const linkedName = "shep"

// linkEnv reads environment variables. os.Getenv is the production
// implementation; tests pass a map lookup so a developer's real XDG_BIN_HOME
// never changes what the tests assert.
type linkEnv func(string) string

// linkDir resolves the directory the published name lives in, in precedence
// order:
//
//  1. SHEP_LINK_DIR — an explicit operator override, honoured only when
//     absolute. A relative override is ignored rather than resolved against the
//     current directory, because "where a PATH name lives" must not depend on
//     the cwd the verb happened to run from.
//  2. XDG_BIN_HOME — the XDG user-binary directory, same absolute-only rule.
//  3. ~/.local/bin — the default on both macOS and Linux.
//
// There is deliberately no per-OS branch. ~/.local/bin needs no sudo, is
// per-user, and is identical on both platforms; /usr/local/bin requires root and
// is not Homebrew's prefix on Apple Silicon, /opt/homebrew/bin belongs to
// Homebrew and is macOS-ARM only, and /usr/bin is owned by the system and
// protected by SIP on macOS.
func linkDir(home string, env linkEnv) string {
	for _, key := range []string{"SHEP_LINK_DIR", "XDG_BIN_HOME"} {
		if dir := strings.TrimSpace(env(key)); filepath.IsAbs(dir) {
			return filepath.Clean(dir)
		}
	}
	return filepath.Join(home, ".local", "bin")
}

// linkPath is the published name itself.
func linkPath(home string, env linkEnv) string {
	return filepath.Join(linkDir(home, env), linkedName)
}

// linkProbeKind classifies what sits at the destination.
type linkProbeKind int

const (
	// probeAbsent means nothing is at the path.
	probeAbsent linkProbeKind = iota
	// probeSymlink means the path is a symlink; Target carries what it names.
	probeSymlink
	// probeOther means the path is occupied by something that is not a symlink.
	probeOther
)

// linkProbe is what the destination holds, read WITHOUT following the final
// symlink. Following it would report the binary when the entire question is what
// the NAME is, and a dangling link would read as absent.
type linkProbe struct {
	Kind linkProbeKind
	// Target is the link's target made absolute the way the kernel resolves it:
	// relative to the link's own directory. It is deliberately not fully
	// resolved, so a link naming an install that is currently absent is still
	// classified by what it names rather than collapsing to absent.
	Target string
	// What describes a probeOther in the words the refusal prints, e.g.
	// "a regular file", "a directory".
	What string
}

// resolveLinkTarget makes a raw symlink target absolute the way the kernel does.
func resolveLinkTarget(linkAt, rawTarget string) string {
	if filepath.IsAbs(rawTarget) {
		return filepath.Clean(rawTarget)
	}
	return filepath.Join(filepath.Dir(linkAt), rawTarget)
}

// isShepBinaryPath reports whether target names SOME Shep install's binary.
// That — not equality with our own — is what marks a destination as one Shep
// published and may therefore replace.
func isShepBinaryPath(target string) bool {
	return filepath.Base(target) == linkedName &&
		filepath.Base(filepath.Dir(target)) == "bin"
}

// linkAction is what `link` decided to do.
type linkAction int

const (
	// linkCreate means nothing was there; create the symlink.
	linkCreate linkAction = iota
	// linkKeep means it already points at this install's binary; do nothing.
	linkKeep
	// linkReplace means another Shep install owned the name; take it over.
	linkReplace
	// linkRefuse means the destination is not ours to touch.
	linkRefuse
)

// linkVerdict is the decision plus the one fact its message needs.
type linkVerdict struct {
	Action linkAction
	// Previous is the target being taken over, set only for linkReplace.
	Previous string
	// Reason explains a linkRefuse in the words the error prints.
	Reason string
}

// classifyLink is the `link` decision as a total function of what is at the
// destination and what we would publish. Keeping it pure is what makes every
// branch — including the refusals that must never touch the filesystem —
// testable without creating a single file.
func classifyLink(probe linkProbe, own string) linkVerdict {
	switch probe.Kind {
	case probeAbsent:
		return linkVerdict{Action: linkCreate}
	case probeSymlink:
		if probe.Target == own {
			return linkVerdict{Action: linkKeep}
		}
		if isShepBinaryPath(probe.Target) {
			return linkVerdict{Action: linkReplace, Previous: probe.Target}
		}
		return linkVerdict{
			Action: linkRefuse,
			Reason: fmt.Sprintf("a symlink to %s, which is not a shep binary", probe.Target),
		}
	default:
		return linkVerdict{Action: linkRefuse, Reason: probe.What}
	}
}

// unlinkAction is what `unlink` decided to do.
type unlinkAction int

const (
	// unlinkRemove means the name is ours and will be removed.
	unlinkRemove unlinkAction = iota
	// unlinkAbsent means there is nothing to remove.
	unlinkAbsent
	// unlinkRefuse means the name belongs to something else.
	unlinkRefuse
)

type unlinkVerdict struct {
	Action unlinkAction
	Reason string
}

// classifyUnlink is the `unlink` decision. Only this install's own link is ours
// to remove: a link to another install is that install's to manage, and
// anything that is not a symlink was never ours at all.
func classifyUnlink(probe linkProbe, own string) unlinkVerdict {
	switch probe.Kind {
	case probeAbsent:
		return unlinkVerdict{Action: unlinkAbsent}
	case probeSymlink:
		if probe.Target == own {
			return unlinkVerdict{Action: unlinkRemove}
		}
		if isShepBinaryPath(probe.Target) {
			return unlinkVerdict{
				Action: unlinkRefuse,
				Reason: fmt.Sprintf("it points at %s — that install owns the name", probe.Target),
			}
		}
		return unlinkVerdict{
			Action: unlinkRefuse,
			Reason: fmt.Sprintf("it points at %s, which shep never published", probe.Target),
		}
	default:
		return unlinkVerdict{Action: unlinkRefuse, Reason: probe.What}
	}
}

// onPath reports whether dir is on pathVar. The comparison is a plain split with
// trailing separators trimmed, never a resolve or a glob: what matters is
// whether the operator's shell would find the name, and the shell does exactly
// this comparison.
func onPath(dir, pathVar string) bool {
	if pathVar == "" {
		return false
	}
	want := trimTrailingSeparator(dir)
	for _, entry := range filepath.SplitList(pathVar) {
		if entry == "" {
			continue
		}
		if trimTrailingSeparator(entry) == want {
			return true
		}
	}
	return false
}

func trimTrailingSeparator(p string) string {
	for len(p) > 1 && os.IsPathSeparator(p[len(p)-1]) {
		p = p[:len(p)-1]
	}
	return p
}
