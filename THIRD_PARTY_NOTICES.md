# Third-party notices

shep is distributed under the MIT License (see `LICENSE`). It includes the
following third-party material.

## Herdr

- Project: Herdr, https://github.com/herdrdev/herdr
- Version: v0.9.3
- License: Apache License, Version 2.0
  (https://www.apache.org/licenses/LICENSE-2.0)
- Copyright: the Herdr authors

`internal/theme/palettes.go` contains the 18 built-in color palettes of
Herdr's `src/app/state.rs` (token values and per-theme descriptions), and
`internal/theme/palette.go` and `internal/theme/herdr.go` port Herdr's theme
names and aliases (`canonical_theme_name`), its light/dark sibling names, its
color syntax (`parse_color`) and its theme resolution order, so that shep's
`inherit` theme renders exactly like Herdr. The palette values are copied
unchanged; the code is a Go reimplementation. The test fixture
`internal/theme/testdata/herdr-palettes.json` holds the same palettes as
extracted from Herdr's source.

Licensed under the Apache License, Version 2.0 (the "License"); you may not
use these files except in compliance with the License. You may obtain a copy
of the License at https://www.apache.org/licenses/LICENSE-2.0. Unless
required by applicable law or agreed to in writing, software distributed
under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
CONDITIONS OF ANY KIND, either express or implied. See the License for the
specific language governing permissions and limitations under the License.

## Catppuccin

- Project: Catppuccin palette, https://github.com/catppuccin/palette
- License: MIT

The `catppuccin-frappe` and `catppuccin-macchiato` palettes in
`internal/theme/palettes.go` use the official Catppuccin Frappé and
Macchiato color values.
