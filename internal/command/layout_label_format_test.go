package command

import (
	"testing"

	"github.com/tranceh2/shep/internal/config"
)

func TestLayoutFromConfig_ThreadsLabelFormats(t *testing.T) {
	sources := config.SourcesConfig{
		Herdr: config.HerdrSourceConfig{
			LabelFormat:     "workspace={{.Path}}",
			TabLabelFormat:  "tab={{.Label}}",
			PaneLabelFormat: "pane={{.Path}}",
		},
		Workspaces: config.WorkspacesSourceConfig{LabelFormat: "entry={{.Label}}"},
		Zoxide:     config.ZoxideSourceConfig{LabelFormat: "history={{.Path}}"},
		Projects:   config.ProjectsSourceConfig{LabelFormat: "project={{.Label}}"},
	}

	layout := layoutFromConfig(config.TUIConfig{}, nil, sources)
	if got, want := layout.LabelFormats.Herdr, "workspace={{.Path}}"; got != want {
		t.Errorf("Herdr format = %q, want %q", got, want)
	}
	if got, want := layout.LabelFormats.Workspaces, "entry={{.Label}}"; got != want {
		t.Errorf("Workspaces format = %q, want %q", got, want)
	}
	if got, want := layout.LabelFormats.Zoxide, "history={{.Path}}"; got != want {
		t.Errorf("Zoxide format = %q, want %q", got, want)
	}
	if got, want := layout.LabelFormats.Projects, "project={{.Label}}"; got != want {
		t.Errorf("Projects format = %q, want %q", got, want)
	}
	if got, want := layout.LabelFormats.Tab, "tab={{.Label}}"; got != want {
		t.Errorf("Tab format = %q, want %q", got, want)
	}
	if got, want := layout.LabelFormats.Pane, "pane={{.Path}}"; got != want {
		t.Errorf("Pane format = %q, want %q", got, want)
	}
}
