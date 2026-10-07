package theme

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Custom is one [themes.<name>] table of shep's configuration. Keys are
// token and role names as written in TOML.
type Custom struct {
	// Base is the theme this one extends: a built-in name or alias,
	// "inherit" or another custom theme. Empty means "catppuccin".
	Base string
	// Tokens overrides palette tokens: token name to color (Herdr syntax).
	Tokens map[string]string
	// Roles overrides roles: role name to a token, role or color.
	Roles map[string]string
}

// layer is a theme under construction: the palette and role references
// accumulated along a base chain.
type layer struct {
	palette Palette
	refs    [numRoles]ref
	noColor bool
	herdr   string
}

type builder struct {
	customs map[string]Custom
	herdr   HerdrTheme
	dark    bool
}

// Build resolves the theme called name: "inherit" (Herdr's theme from herdr,
// for the dark or light appearance), a built-in name or alias, "plain", or a
// key of customs. A custom theme starts from its base, resolved recursively
// (default "catppuccin"), then applies its token overrides, then its role
// overrides; role references are resolved last, so a role may refer to a
// token the same theme overrides. Errors name the theme and key, e.g.
// `themes.mine.accent: invalid color "#12": …`.
func Build(name string, customs map[string]Custom, herdr HerdrTheme, dark bool) (Theme, error) {
	b := builder{customs: customs, herdr: herdr, dark: dark}
	var kind SourceKind
	themeName := name
	switch canonical, builtin := CanonicalName(name); {
	case name == NameInherit:
		kind = SourceInherit
	case b.isCustom(name):
		kind = SourceCustom
	case builtin:
		kind, themeName = SourceBuiltin, canonical
	default:
		return Theme{}, b.unknownTheme(name)
	}
	l, err := b.layer(name, nil)
	if err != nil {
		return Theme{}, err
	}
	t := Theme{
		Name:    themeName,
		NoColor: l.noColor,
		Source:  Source{Kind: kind, Herdr: l.herdr},
	}
	if l.noColor {
		return t, nil
	}
	roles, err := resolveRoles(l.palette, &l.refs)
	if err != nil {
		return Theme{}, fmt.Errorf("themes.%s.roles: %w", name, err)
	}
	t.palette, t.roles = l.palette, roles
	return t, nil
}

// Validate checks every custom theme (names, bases, tokens, roles and
// cycles) independently of which one is selected, with inherit read as
// Herdr's defaults. It returns all distinct problems joined, in theme-name
// order.
func Validate(customs map[string]Custom) error {
	var errs []error
	seen := make(map[string]bool)
	add := func(err error) {
		if err != nil && !seen[err.Error()] {
			seen[err.Error()] = true
			errs = append(errs, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(customs)) {
		if err := checkCustomName(name); err != nil {
			add(err)
			continue
		}
		_, err := Build(name, customs, HerdrTheme{}, true)
		add(err)
	}
	return errors.Join(errs...)
}

// checkCustomName rejects custom theme names that would shadow a built-in
// name, alias or selector.
func checkCustomName(name string) error {
	if name == "" {
		return errors.New("themes: a theme name must not be empty")
	}
	if name == NameInherit {
		return fmt.Errorf("themes.%s: the name is reserved for Herdr's theme", name)
	}
	if canonical, ok := CanonicalName(name); ok {
		return fmt.Errorf("themes.%s: the name is reserved by the built-in theme %q", name, canonical)
	}
	return nil
}

func (b *builder) isCustom(name string) bool {
	_, ok := b.customs[name]
	return ok
}

func (b *builder) known(name string) bool {
	if name == NameInherit || b.isCustom(name) {
		return true
	}
	_, ok := CanonicalName(name)
	return ok
}

// layer resolves name; chain holds the custom themes whose base led here.
func (b *builder) layer(name string, chain []string) (layer, error) {
	if name == NameInherit {
		return layer{
			palette: b.herdr.Resolve(b.dark),
			refs:    defaultRefs(),
			herdr:   b.herdr.ThemeName(b.dark),
		}, nil
	}
	c, ok := b.customs[name]
	if !ok {
		canonical, ok := CanonicalName(name)
		if !ok {
			return layer{}, b.unknownTheme(name)
		}
		if canonical == NamePlain {
			return layer{noColor: true}, nil
		}
		return layer{palette: builtinPalettes[canonical], refs: defaultRefs()}, nil
	}
	if err := checkCustomName(name); err != nil {
		return layer{}, err
	}
	if i := slices.Index(chain, name); i >= 0 {
		cycle := append(slices.Clone(chain[i:]), name)
		return layer{}, fmt.Errorf("themes.%s.base: cycle %s", chain[len(chain)-1], strings.Join(cycle, " -> "))
	}
	base := c.Base
	if base == "" {
		base = NameDefault
	}
	if !b.known(base) {
		return layer{}, fmt.Errorf("themes.%s.base: %w", name, b.unknownTheme(base))
	}
	if canonical, _ := CanonicalName(base); canonical == NamePlain && !b.isCustom(base) {
		return layer{}, fmt.Errorf("themes.%s.base: %q has no colors to extend; use \"terminal\" for the terminal's own colors", name, base)
	}
	l, err := b.layer(base, append(chain, name))
	if err != nil {
		return layer{}, err
	}
	for _, k := range slices.Sorted(maps.Keys(c.Tokens)) {
		t, ok := ParseToken(k)
		if !ok {
			return layer{}, fmt.Errorf("themes.%s.%s: unknown token %q (want one of %s)", name, k, k, tokenList())
		}
		col, err := ParseColor(c.Tokens[k])
		if err != nil {
			return layer{}, fmt.Errorf("themes.%s.%s: %w", name, k, err)
		}
		l.palette = l.palette.With(t, col)
	}
	for _, k := range slices.Sorted(maps.Keys(c.Roles)) {
		r, ok := ParseRole(k)
		if !ok {
			return layer{}, fmt.Errorf("themes.%s.roles.%s: unknown role %q", name, k, k)
		}
		v, err := parseRef(c.Roles[k])
		if err != nil {
			return layer{}, fmt.Errorf("themes.%s.roles.%s: %w", name, k, err)
		}
		l.refs[r] = v
	}
	return l, nil
}

func (b *builder) unknownTheme(name string) error {
	msg := fmt.Sprintf("unknown theme %q; want %s, a built-in theme (%s)", name, NameInherit, strings.Join(BuiltinNames(), ", "))
	if len(b.customs) > 0 {
		msg += " or a custom theme (" + strings.Join(slices.Sorted(maps.Keys(b.customs)), ", ") + ")"
	} else {
		msg += " or a [themes.<name>] table"
	}
	return errors.New(msg)
}
