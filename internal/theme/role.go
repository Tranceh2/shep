package theme

import (
	"fmt"
	"strings"
)

// Role is a shep semantic color: what a piece of the picker means rather
// than which palette color it uses. Every role defaults to one palette token
// (see the package documentation) and can be remapped per custom theme.
type Role uint8

const (
	// RoleText is ordinary text.
	RoleText Role = iota
	// RoleTextSecondary is secondary text: paths, metadata, hint labels.
	RoleTextSecondary
	// RoleTextMuted is low-priority text: counts, unknown values.
	RoleTextMuted
	// RoleAccent is the generic focus/highlight color.
	RoleAccent
	// RoleRule colors rules, dividers and separators.
	RoleRule
	// RoleSelection is the background of the cursor row.
	RoleSelection
	// RoleTabActive is the background of the active tab.
	RoleTabActive
	// RoleTabActiveFg is the foreground of the active tab.
	RoleTabActiveFg
	// RolePrompt colors the query prompt.
	RolePrompt
	// RoleCursor colors the cursor-row gutter and the query cursor.
	RoleCursor
	// RoleMatch colors the characters matched by the search.
	RoleMatch
	// RoleHeading colors section and help headings.
	RoleHeading
	// RoleRowLabel colors a row's name (label_format).
	RoleRowLabel
	// RoleRowDetail colors a row's dim context (detail_format).
	RoleRowDetail
	// RoleRowMarker colors a row's right-aligned text (marker_format).
	RoleRowMarker
	// RoleRowDescendant colors rows nested under a group or workspace.
	RoleRowDescendant
	// RoleStatusWorking is the agent "working" state.
	RoleStatusWorking
	// RoleStatusBlocked is the agent "blocked" (needs attention) state.
	RoleStatusBlocked
	// RoleStatusDone is the agent "done" state.
	RoleStatusDone
	// RoleStatusIdle is the agent "idle" state.
	RoleStatusIdle
	// RoleStatusUnknown is an unknown agent state.
	RoleStatusUnknown
	// RoleSourceHerdr is the icon color of open Herdr workspaces.
	RoleSourceHerdr
	// RoleSourceWorkspaces is the icon color of configured workspaces.
	RoleSourceWorkspaces
	// RoleSourceZoxide is the icon color of zoxide folders.
	RoleSourceZoxide
	// RoleSourceProjects is the icon color of projects and worktrees.
	RoleSourceProjects
	// RoleSourceSessions is the icon color of sessions.
	RoleSourceSessions
	// RoleSourceAgents is the icon color of agent rows.
	RoleSourceAgents
	// RoleSourceCustom is the icon color of custom-source rows.
	RoleSourceCustom
	// RolePin colors the pin marker.
	RolePin
	// RoleGitBranch colors branch names.
	RoleGitBranch
	// RoleGitClean marks a clean working tree.
	RoleGitClean
	// RoleGitChanges marks a working tree with changes.
	RoleGitChanges
	// RoleError colors errors.
	RoleError
	// RoleWarning colors warnings and confirmations.
	RoleWarning
	// RoleSuccess colors success messages.
	RoleSuccess

	// numRoles is the number of roles.
	numRoles = iota
)

// roleNames are the role names as written in [themes.<name>.roles] and in
// color references.
var roleNames = [numRoles]string{
	RoleText:             "text",
	RoleTextSecondary:    "text.secondary",
	RoleTextMuted:        "text.muted",
	RoleAccent:           "accent",
	RoleRule:             "rule",
	RoleSelection:        "selection",
	RoleTabActive:        "tab.active",
	RoleTabActiveFg:      "tab.active.fg",
	RolePrompt:           "prompt",
	RoleCursor:           "cursor",
	RoleMatch:            "match",
	RoleHeading:          "heading",
	RoleRowLabel:         "row.label",
	RoleRowDetail:        "row.detail",
	RoleRowMarker:        "row.marker",
	RoleRowDescendant:    "row.descendant",
	RoleStatusWorking:    "status.working",
	RoleStatusBlocked:    "status.blocked",
	RoleStatusDone:       "status.done",
	RoleStatusIdle:       "status.idle",
	RoleStatusUnknown:    "status.unknown",
	RoleSourceHerdr:      "source.herdr",
	RoleSourceWorkspaces: "source.workspaces",
	RoleSourceZoxide:     "source.zoxide",
	RoleSourceProjects:   "source.projects",
	RoleSourceSessions:   "source.sessions",
	RoleSourceAgents:     "source.agents",
	RoleSourceCustom:     "source.custom",
	RolePin:              "pin",
	RoleGitBranch:        "git.branch",
	RoleGitClean:         "git.clean",
	RoleGitChanges:       "git.changes",
	RoleError:            "error",
	RoleWarning:          "warning",
	RoleSuccess:          "success",
}

// defaultRoleTokens is the default token of every role (the table in the
// package documentation).
var defaultRoleTokens = [numRoles]Token{
	RoleText:             TokenText,
	RoleTextSecondary:    TokenSubtext0,
	RoleTextMuted:        TokenOverlay0,
	RoleAccent:           TokenAccent,
	RoleRule:             TokenSurface1,
	RoleSelection:        TokenSelectionBg,
	RoleTabActive:        TokenSelectionBg,
	RoleTabActiveFg:      TokenAccent,
	RolePrompt:           TokenAccent,
	RoleCursor:           TokenAccent,
	RoleMatch:            TokenAccent,
	RoleHeading:          TokenAccent,
	RoleRowLabel:         TokenText,
	RoleRowDetail:        TokenOverlay0,
	RoleRowMarker:        TokenOverlay0,
	RoleRowDescendant:    TokenOverlay0,
	RoleStatusWorking:    TokenYellow,
	RoleStatusBlocked:    TokenRed,
	RoleStatusDone:       TokenTeal,
	RoleStatusIdle:       TokenGreen,
	RoleStatusUnknown:    TokenOverlay0,
	RoleSourceHerdr:      TokenGreen,
	RoleSourceWorkspaces: TokenMauve,
	RoleSourceZoxide:     TokenBlue,
	RoleSourceProjects:   TokenPeach,
	RoleSourceSessions:   TokenYellow,
	RoleSourceAgents:     TokenAccent,
	RoleSourceCustom:     TokenTeal,
	RolePin:              TokenYellow,
	RoleGitBranch:        TokenMauve,
	RoleGitClean:         TokenGreen,
	RoleGitChanges:       TokenYellow,
	RoleError:            TokenRed,
	RoleWarning:          TokenYellow,
	RoleSuccess:          TokenGreen,
}

// String returns the role's configuration name, e.g. "row.detail".
func (r Role) String() string {
	if int(r) < numRoles {
		return roleNames[r]
	}
	return fmt.Sprintf("Role(%d)", uint8(r))
}

// ParseRole returns the role with the given configuration name. Names are
// matched exactly.
func ParseRole(name string) (Role, bool) {
	for i, n := range roleNames {
		if n == name {
			return Role(i), true
		}
	}
	return 0, false
}

// Roles returns every role in declaration order.
func Roles() []Role {
	out := make([]Role, numRoles)
	for i := range out {
		out[i] = Role(i)
	}
	return out
}

// DefaultToken returns the palette token role r maps to when no theme
// overrides it.
func DefaultToken(r Role) Token {
	if int(r) >= numRoles {
		return TokenText
	}
	return defaultRoleTokens[r]
}

type refKind uint8

const (
	refToken refKind = iota
	refRole
	refColor
)

// ref is a parsed color reference: a palette token, a role or a color
// literal.
type ref struct {
	kind  refKind
	token Token
	role  Role
	color Color
}

// parseRef parses a color reference. Lookup order is token, then role, then
// color literal, so "text" and "accent" (both token and role names) mean the
// tokens, and "red", "green", "yellow" and "blue" mean the palette tokens
// rather than the terminal colors of the same names. Like color literals,
// token and role names ignore surrounding spaces and letter case, so "Blue"
// is the token too.
func parseRef(s string) (ref, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if t, ok := ParseToken(v); ok {
		return ref{kind: refToken, token: t}, nil
	}
	if r, ok := ParseRole(v); ok {
		return ref{kind: refRole, role: r}, nil
	}
	c, err := ParseColor(s)
	if err == nil {
		return ref{kind: refColor, color: c}, nil
	}
	if strings.HasPrefix(v, "#") || strings.HasPrefix(v, "rgb(") {
		return ref{}, err
	}
	return ref{}, fmt.Errorf("unknown token or role %q (want a palette token such as \"blue\", a role such as \"source.zoxide\", or a color such as \"#89b4fa\")", s)
}

// ValidateRef reports whether s is a valid color reference (a palette token,
// a role or a color literal) without needing a theme, for configuration
// validation of settings such as icon_color.
func ValidateRef(s string) error {
	_, err := parseRef(s)
	return err
}

// resolveRoles computes every role's color from its reference, following
// role-to-role references and reporting the first cycle found.
func resolveRoles(p Palette, refs *[numRoles]ref) ([numRoles]Color, error) {
	var out [numRoles]Color
	var done [numRoles]bool
	for start := range numRoles {
		if done[start] {
			continue
		}
		path := []Role{Role(start)}
		cur := refs[start]
		var c Color
		for {
			if cur.kind != refRole {
				if cur.kind == refToken {
					c = p.Get(cur.token)
				} else {
					c = cur.color
				}
				break
			}
			if done[cur.role] {
				c = out[cur.role]
				break
			}
			for i, r := range path {
				if r == cur.role {
					return out, fmt.Errorf("cycle %s", cyclePath(append(path[i:], cur.role)))
				}
			}
			path = append(path, cur.role)
			cur = refs[cur.role]
		}
		for _, r := range path {
			out[r] = c
			done[r] = true
		}
	}
	return out, nil
}

func cyclePath(roles []Role) string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.String()
	}
	return strings.Join(names, " -> ")
}

// defaultRefs returns the default reference of every role.
func defaultRefs() [numRoles]ref {
	var out [numRoles]ref
	for i, t := range defaultRoleTokens {
		out[i] = ref{kind: refToken, token: t}
	}
	return out
}
