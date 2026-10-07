package theme

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Options are the inputs of Select.
type Options struct {
	// ConfigTheme is [tui].theme; empty means inherit.
	ConfigTheme string
	// Customs are the [themes.<name>] tables.
	Customs map[string]Custom
	// Getenv reads NO_COLOR and SHEP_THEME; nil means os.Getenv.
	Getenv func(string) string
	// HerdrConfigPath is Herdr's config.toml (see DefaultHerdrConfigPath);
	// empty means there is none, so inherit uses Herdr's defaults.
	HerdrConfigPath string
	// Light selects the variant for a light terminal appearance. Only a
	// theme inheriting a Herdr configuration with auto_switch on has one (see
	// Source.FollowsAppearance); every other theme ignores it.
	Light bool
}

// Select picks the theme shep renders with. Precedence:
//
//  1. NO_COLOR set to any non-empty value: the no-color theme.
//  2. SHEP_THEME: a built-in name or alias, "inherit", "plain" or a custom
//     theme. An unknown or invalid value is ignored, never an error (an
//     environment variable must not prevent startup), and recorded in
//     Source.Notes.
//  3. ConfigTheme: "inherit" (also when empty), a built-in name or alias,
//     "plain" or a custom theme. An unknown or invalid value is an error;
//     configuration validation reports it first.
//
// Inheriting reads the Herdr configuration; when it is missing or unreadable
// the theme is Herdr's default (catppuccin) and Source.Notes says why.
func Select(opts Options) (Theme, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv("NO_COLOR") != "" {
		return Theme{
			Name:    NamePlain,
			NoColor: true,
			Source:  Source{Kind: SourceNoColor, Setting: "NO_COLOR"},
		}, nil
	}
	s := selector{opts: opts}
	var notes []string
	if env := getenv("SHEP_THEME"); env != "" {
		t, err := s.build(env)
		if err == nil {
			t.Source.Setting = "SHEP_THEME"
			return t, nil
		}
		notes = append(notes, fmt.Sprintf("SHEP_THEME=%q ignored: %v", env, err))
	}
	name, setting := opts.ConfigTheme, "tui.theme"
	if name == "" {
		name, setting = NameInherit, "default"
	}
	t, err := s.build(name)
	if err != nil {
		return Theme{}, fmt.Errorf("tui.theme: %w", err)
	}
	t.Source.Setting = setting
	t.Source.Notes = append(notes, t.Source.Notes...)
	return t, nil
}

// selector reads the Herdr configuration at most once per Select.
type selector struct {
	opts   Options
	loaded bool
	herdr  HerdrTheme
	dark   bool
	notes  []string
}

func (s *selector) build(name string) (Theme, error) {
	if !inheritsHerdr(name, s.opts.Customs) {
		return Build(name, s.opts.Customs, HerdrTheme{}, true)
	}
	s.loadHerdr()
	t, err := Build(name, s.opts.Customs, s.herdr, s.dark)
	if err != nil {
		return Theme{}, err
	}
	t.Source.HerdrPath = s.opts.HerdrConfigPath
	t.Source.FollowsAppearance = s.herdr.AutoSwitch
	t.Source.Notes = append([]string(nil), s.notes...)
	return t, nil
}

func (s *selector) loadHerdr() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.dark = true
	path := s.opts.HerdrConfigPath
	if path == "" {
		s.notes = append(s.notes, "no Herdr configuration path; using Herdr's default theme "+NameDefault)
		return
	}
	h, err := ReadHerdrTheme(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.notes = append(s.notes, fmt.Sprintf("Herdr configuration %s not found; using Herdr's default theme %s", path, NameDefault))
		return
	case err != nil:
		s.notes = append(s.notes, fmt.Sprintf("%v; using Herdr's default theme %s", err, NameDefault))
		return
	}
	s.herdr = h
	s.notes = append(s.notes, h.Diagnostics()...)
	s.dark = !h.AutoSwitch || !s.opts.Light
}

// inheritsHerdr reports whether name is inherit or a custom theme whose base
// chain reaches inherit. A base cycle stops the walk (Build reports it).
func inheritsHerdr(name string, customs map[string]Custom) bool {
	for range len(customs) + 1 {
		if name == NameInherit {
			return true
		}
		c, ok := customs[name]
		if !ok {
			return false
		}
		name = c.Base
		if name == "" {
			name = NameDefault
		}
	}
	return false
}
