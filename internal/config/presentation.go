package config

import (
	"fmt"

	"github.com/tranceh2/shep/internal/theme"
	"github.com/tranceh2/shep/internal/tmpl"
)

// Presentation is how one kind of row is drawn in the picker. Every part is a
// template over the shared data model (internal/tmpl), and may style its text
// (muted, accent, bold) and place live markers (status, pin, current, group,
// missing):
//
//	[icon] label  detail                                   marker
//
// The icon is never truncated; the label is the name the search highlights;
// the detail is dim context that shrinks first; the marker is right-aligned.
// IconColor is a color reference (a palette token, a role or a color).
//
// A nil field is unset and normalization fills it with the built-in default
// (see presentationDefaults); an explicit value, "" included, is kept, so a
// later precedence layer can also blank a part.
type Presentation struct {
	Icon         *string `toml:"icon,omitempty"`
	IconColor    *string `toml:"icon_color,omitempty"`
	LabelFormat  *string `toml:"label_format,omitempty"`
	DetailFormat *string `toml:"detail_format,omitempty"`
	MarkerFormat *string `toml:"marker_format,omitempty"`
}

// RowPresentation is a resolved Presentation: every part's template and the
// icon color, as plain strings.
type RowPresentation struct {
	Icon      string
	IconColor string
	Label     string
	Detail    string
	Marker    string
}

// Resolve returns p with every unset field taken from def.
func (p Presentation) Resolve(def RowPresentation) RowPresentation {
	pick := func(v *string, d string) string {
		if v != nil {
			return *v
		}
		return d
	}
	return RowPresentation{
		Icon:      pick(p.Icon, def.Icon),
		IconColor: pick(p.IconColor, def.IconColor),
		Label:     pick(p.LabelFormat, def.Label),
		Detail:    pick(p.DetailFormat, def.Detail),
		Marker:    pick(p.MarkerFormat, def.Marker),
	}
}

// normalize fills every unset field of p from def.
func (p *Presentation) normalize(def RowPresentation) {
	set := func(v **string, d string) {
		if *v == nil {
			*v = &d
		}
	}
	set(&p.Icon, def.Icon)
	set(&p.IconColor, def.IconColor)
	set(&p.LabelFormat, def.Label)
	set(&p.DetailFormat, def.Detail)
	set(&p.MarkerFormat, def.Marker)
}

// Presentations is the resolved presentation of every kind of row the picker
// draws.
type Presentations struct {
	Herdr      RowPresentation
	HerdrTab   RowPresentation
	HerdrPane  RowPresentation
	Sessions   RowPresentation
	Workspaces RowPresentation
	Zoxide     RowPresentation
	Projects   RowPresentation
	Agents     RowPresentation
	// Custom is keyed by [[sources.custom]].name.
	Custom map[string]RowPresentation
	// Other draws rows whose source has no presentation of its own (a
	// direct --path candidate): its path, name first.
	Other RowPresentation
}

// Presentations returns the configuration's resolved row presentations.
// Fields Load left unset (a hand-built Config) take the built-in defaults
// for [tui].icons.
func (c *Config) Presentations() Presentations {
	icons := c.TUI.Icons
	s := c.Sources
	out := Presentations{
		Herdr:      s.Herdr.Resolve(presentationDefaults(rowHerdr, icons)),
		HerdrTab:   s.Herdr.Tab.Resolve(presentationDefaults(rowHerdrTab, icons)),
		HerdrPane:  s.Herdr.Pane.Resolve(presentationDefaults(rowHerdrPane, icons)),
		Sessions:   s.Sessions.Resolve(presentationDefaults(rowSessions, icons)),
		Workspaces: s.Workspaces.Resolve(presentationDefaults(rowWorkspaces, icons)),
		Zoxide:     s.Zoxide.Resolve(presentationDefaults(rowZoxide, icons)),
		Projects:   s.Projects.Resolve(presentationDefaults(rowProjects, icons)),
		Agents:     s.Agents.Resolve(presentationDefaults(rowAgents, icons)),
		Other:      presentationDefaults(rowOther, icons),
	}
	if len(s.Custom) > 0 {
		out.Custom = make(map[string]RowPresentation, len(s.Custom))
		for _, custom := range s.Custom {
			out.Custom[custom.Name] = custom.Resolve(presentationDefaults(rowCustom, icons))
		}
	}
	return out
}

// DefaultPresentations returns the built-in row presentations for the icons
// tier ([tui].icons), with no custom source.
func DefaultPresentations(icons string) Presentations {
	return (&Config{TUI: TUIConfig{Icons: icons}}).Presentations()
}

// DefaultCustomPresentation returns the built-in presentation of a
// [[sources.custom]] source's rows for the icons tier.
func DefaultCustomPresentation(icons string) RowPresentation {
	return presentationDefaults(rowCustom, icons)
}

// rowKind names one entry of the presentation defaults table.
type rowKind uint8

const (
	rowHerdr rowKind = iota
	rowHerdrTab
	rowHerdrPane
	rowSessions
	rowWorkspaces
	rowZoxide
	rowProjects
	rowAgents
	rowCustom
	rowOther
)

// Default source icons: Nerd Font glyphs, each followed by a space because
// these glyphs often draw wider than the one cell they measure. The ASCII
// tier ([tui].icons = "ascii") has no default source icons: it promises
// 7-bit output.
const (
	iconHerdr      = "\U000f0cc6 "
	iconWorkspaces = "\ue615 "
	iconZoxide     = "\uf114 "
	iconProject    = "\ue702 "
	iconWorktree   = "\ue725 "
)

// The name-first layout every directory-like row uses: the label's last path
// element as the name and its parent as the detail (the path when there is
// no label; home shown as "~"). A label that is not a path is the name alone.
const (
	defaultNameFormat   = "{{ or .Label .Path | tilde | name }}"
	defaultParentFormat = "{{ or .Label .Path | tilde | parent }}"
)

// presentationDefaults is the built-in presentation of every kind of row: the
// single table of row defaults. The glyphs of the picker's own markers
// (status, pin, group) come from the [tui].icons tier when they are drawn;
// the tab icon, the session separator and the source icons are written here
// per tier.
func presentationDefaults(kind rowKind, icons string) RowPresentation {
	tabIcon, separator := "◫", "·"
	herdr, workspaces, zoxide, project, worktree := iconHerdr, iconWorkspaces, iconZoxide, iconProject, iconWorktree
	if icons == TUIIconsASCII {
		tabIcon, separator = "t", "-"
		herdr, workspaces, zoxide, project, worktree = "", "", "", "", ""
	}
	switch kind {
	case rowHerdr:
		return RowPresentation{
			Icon: herdr, IconColor: "source.herdr",
			Label: defaultNameFormat, Detail: defaultParentFormat,
			Marker: "{{ current }} {{ missing }} {{ status }} {{ pin }}",
		}
	case rowHerdrTab:
		return RowPresentation{
			Icon: tabIcon, IconColor: "text.muted",
			Label:  "{{ muted .TabNumber }} {{ if ne .Label .TabNumber }}{{ .Label | tilde }}{{ end }}",
			Marker: "{{ current }}",
		}
	case rowHerdrPane:
		return RowPresentation{
			IconColor: "text.muted",
			Label:     "{{ status }} " + defaultNameFormat, Detail: defaultParentFormat,
			Marker: "{{ current }} {{ if and .Agent (not (contains (lower .Agent) (lower .Label))) }}{{ .Agent }}{{ end }}",
		}
	case rowSessions:
		return RowPresentation{
			IconColor: "source.sessions",
			Label:     defaultNameFormat, Detail: defaultParentFormat,
			Marker: `{{ if eq .Meta.running "true" }}running{{ else }}stopped{{ end }}` +
				`{{ if eq .Meta.default "true" }} ` + separator + ` default{{ end }} {{ missing }} {{ pin }}`,
		}
	case rowWorkspaces:
		return RowPresentation{
			Icon: workspaces, IconColor: "source.workspaces",
			Label: defaultNameFormat, Detail: defaultParentFormat,
			Marker: "{{ missing }} {{ pin }} {{ group }}",
		}
	case rowZoxide:
		return RowPresentation{
			Icon: zoxide, IconColor: "source.zoxide",
			Label: defaultNameFormat, Detail: defaultParentFormat,
			Marker: "{{ missing }} {{ pin }}",
		}
	case rowProjects:
		icon := ""
		if project != "" {
			icon = "{{ if .IsWorktree }}" + worktree + "{{ else }}" + project + "{{ end }}"
		}
		return RowPresentation{
			Icon:      icon,
			IconColor: "source.projects",
			Label:     defaultNameFormat, Detail: defaultParentFormat,
			Marker: "{{ if .IsWorktree }}{{ .Branch }}{{ end }} {{ missing }} {{ pin }}",
		}
	case rowAgents:
		return RowPresentation{
			IconColor: "source.agents",
			Label:     "{{ status }} {{ or .Label .Path | tilde }}",
			Marker:    "{{ .Workspace | trimIcon | name }}",
		}
	case rowCustom:
		return RowPresentation{
			Icon: "{{ .Icon }}", IconColor: "source.custom",
			Label: defaultNameFormat, Detail: defaultParentFormat,
			Marker: "{{ missing }} {{ pin }}",
		}
	default:
		return RowPresentation{
			Icon: "{{ .Icon }}", IconColor: "source.custom",
			Label: "{{ .Path | tilde | name }}", Detail: "{{ .Path | tilde | parent }}",
			Marker: "{{ missing }} {{ pin }}",
		}
	}
}

// normalizePresentations fills every unset presentation field of the built-in
// sources with its default for the [tui].icons tier.
func normalizePresentations(s *SourcesConfig, icons string) {
	s.Herdr.normalize(presentationDefaults(rowHerdr, icons))
	s.Herdr.Tab.normalize(presentationDefaults(rowHerdrTab, icons))
	s.Herdr.Pane.normalize(presentationDefaults(rowHerdrPane, icons))
	s.Sessions.normalize(presentationDefaults(rowSessions, icons))
	s.Workspaces.normalize(presentationDefaults(rowWorkspaces, icons))
	s.Zoxide.normalize(presentationDefaults(rowZoxide, icons))
	s.Projects.normalize(presentationDefaults(rowProjects, icons))
	s.Agents.normalize(presentationDefaults(rowAgents, icons))
	for i := range s.Custom {
		s.Custom[i].normalize(presentationDefaults(rowCustom, icons))
	}
}

// validatePresentation checks every set field of p: each template against
// representative data of the kinds of rows it draws, the icon color as a
// color reference. field is the table's path ("sources.herdr.tab").
func validatePresentation(engine *tmpl.Engine, field string, p Presentation, kinds ...string) error {
	samples := tmpl.Samples(kinds...)
	for _, part := range []struct {
		key    string
		format *string
	}{
		{"icon", p.Icon},
		{"label_format", p.LabelFormat},
		{"detail_format", p.DetailFormat},
		{"marker_format", p.MarkerFormat},
	} {
		if part.format == nil {
			continue
		}
		if err := engine.Validate(field+"."+part.key, *part.format, samples...); err != nil {
			return err
		}
	}
	if p.IconColor != nil {
		if err := theme.ValidateRef(*p.IconColor); err != nil {
			return fmt.Errorf("%s.icon_color: %w", field, err)
		}
	}
	return nil
}

// validatePresentations validates the built-in sources' presentations, each
// against the kinds of rows it draws. Custom sources are validated with the
// rest of their table (see validateCustomSources).
func validatePresentations(s SourcesConfig, engine *tmpl.Engine) error {
	for _, p := range []struct {
		field string
		p     Presentation
		kinds []string
	}{
		{"sources.herdr", s.Herdr.Presentation, []string{tmpl.KindWorkspace}},
		{"sources.herdr.tab", s.Herdr.Tab, []string{tmpl.KindTab}},
		{"sources.herdr.pane", s.Herdr.Pane, []string{tmpl.KindPane}},
		{"sources.sessions", s.Sessions.Presentation, []string{tmpl.KindSession}},
		{"sources.workspaces", s.Workspaces.Presentation, []string{tmpl.KindConfigured, tmpl.KindGroup}},
		{"sources.zoxide", s.Zoxide.Presentation, []string{tmpl.KindFolder}},
		{"sources.projects", s.Projects.Presentation, []string{tmpl.KindProject, tmpl.KindWorktree}},
		{"sources.agents", s.Agents.Presentation, []string{tmpl.KindAgent}},
	} {
		if err := validatePresentation(engine, p.field, p.p, p.kinds...); err != nil {
			return err
		}
	}
	return nil
}
