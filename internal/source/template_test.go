package source

import (
	"reflect"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/tmpl"
)

func TestTemplateData_KindPerSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cand Candidate
		want string
	}{
		{"open herdr workspace", Candidate{Source: config.SourceHerdr}, tmpl.KindWorkspace},
		{"configured workspace", Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"command": "nvim"}}, tmpl.KindConfigured},
		{"configured workspace without meta", Candidate{Source: config.SourceWorkspaces}, tmpl.KindConfigured},
		{"workspace group", Candidate{Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}, tmpl.KindGroup},
		{"zoxide folder", Candidate{Source: config.SourceZoxide}, tmpl.KindFolder},
		{"direct path", Candidate{Source: "path"}, tmpl.KindFolder},
		{"project", Candidate{Source: config.SourceProjects}, tmpl.KindProject},
		{"main checkout with worktrees", Candidate{Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "main_worktree": "true"}}, tmpl.KindWorktree},
		{"linked worktree", Candidate{Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true"}}, tmpl.KindWorktree},
		{"session", Candidate{Source: config.SourceSessions}, tmpl.KindSession},
		{"agent", Candidate{Source: config.SourceAgents}, tmpl.KindAgent},
		{"custom source", Candidate{Source: "prs", Meta: map[string]string{"custom_source": "true"}}, tmpl.KindCustom},
		{"tree row leaves kind to the caller", Candidate{Meta: map[string]string{"tab_id": "t1"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TemplateData(tc.cand).Kind; got != tc.want {
				t.Fatalf("Kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTemplateData_Worktree(t *testing.T) {
	t.Parallel()
	meta := map[string]string{
		"is_worktree": "true", "main_worktree": "false", "branch": "feature/x",
		"repo": "shep", "head": "abcdef0123456789", "worktree_path": "/trees/shep-x",
	}
	c := Candidate{
		Path: "/trees/shep-x", NormalizedPath: "/real/trees/shep-x", Label: "shep (feature/x)",
		Source: config.SourceProjects, Icon: "W", Meta: meta,
	}
	want := tmpl.Data{
		Path: "/trees/shep-x", NormalizedPath: "/real/trees/shep-x", Label: "shep (feature/x)",
		Source: config.SourceProjects, Kind: tmpl.KindWorktree, Icon: "W",
		Branch: "feature/x", Head: "abcdef0", RepoName: "shep", IsWorktree: true, Meta: meta,
	}
	if got := TemplateData(c); !reflect.DeepEqual(got, want) {
		t.Fatalf("TemplateData =\n%+v\nwant\n%+v", got, want)
	}
}

func TestTemplateData_BranchFallbacks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		meta       map[string]string
		wantBranch string
		wantHead   string
		wantMain   bool
	}{
		{"detached worktree uses short head", map[string]string{"is_worktree": "true", "head": "1234567890abcdef"}, "1234567", "1234567", false},
		{"detached worktree without head", map[string]string{"is_worktree": "true"}, "detached", "", false},
		{"short head kept whole", map[string]string{"is_worktree": "true", "head": "abc"}, "abc", "abc", false},
		{"main checkout", map[string]string{"is_worktree": "true", "main_worktree": "true", "branch": "main", "head": "1111222233334444"}, "main", "1111222", true},
		{"branch outside a worktree", map[string]string{"branch": "dev"}, "dev", "", false},
		{"head outside a worktree is not a branch", map[string]string{"head": "1234567890"}, "", "1234567", false},
		{"no metadata", nil, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TemplateData(Candidate{Source: config.SourceProjects, Meta: tc.meta})
			if got.Branch != tc.wantBranch || got.Head != tc.wantHead || got.IsMainWorktree != tc.wantMain {
				t.Fatalf("Branch/Head/IsMainWorktree = %q/%q/%v, want %q/%q/%v", got.Branch, got.Head, got.IsMainWorktree, tc.wantBranch, tc.wantHead, tc.wantMain)
			}
		})
	}
}

func TestTemplateData_HerdrRowFields(t *testing.T) {
	t.Parallel()
	agent := TemplateData(Candidate{
		Path: "/srv/api", Label: "claude: fix", Source: config.SourceAgents,
		Meta: map[string]string{
			"agent": "claude", "agent_status": "blocked", "tab_label": "agents",
			"workspace_label": "~/srv/api", "kind": "agent",
		},
	})
	if agent.Agent != "claude" || agent.AgentStatus != "blocked" || agent.TabLabel != "agents" || agent.Workspace != "~/srv/api" {
		t.Fatalf("agent data = %+v", agent)
	}
	tab := TemplateData(Candidate{
		Label: "editor", Path: "/srv/api",
		Meta: map[string]string{"tab_number": "2", "tab_label": "editor", "workspace_label": "api"},
	})
	if tab.TabNumber != "2" || tab.TabLabel != "editor" || tab.Workspace != "api" || tab.Kind != "" {
		t.Fatalf("tab data = %+v", tab)
	}
}

func TestTemplateData_SharesMetaWithoutMutatingIt(t *testing.T) {
	t.Parallel()
	meta := map[string]string{"is_worktree": "true", "repo": "shep"}
	got := TemplateData(Candidate{Source: config.SourceProjects, Meta: meta})
	if len(meta) != 2 || meta["branch"] != "" {
		t.Fatalf("TemplateData mutated the candidate's Meta: %v", meta)
	}
	if reflect.ValueOf(got.Meta).Pointer() != reflect.ValueOf(meta).Pointer() {
		t.Fatal("TemplateData copied Meta; want the candidate's map shared read-only")
	}
}

func TestTemplateData_RendersThroughEngine(t *testing.T) {
	t.Parallel()
	e := tmpl.New("/home/me")
	c := Candidate{
		Path: "/home/me/Proyectos/shep", Label: "~/Proyectos/shep", Source: config.SourceZoxide,
		Meta: map[string]string{"custom": "value"},
	}
	got, err := e.Render(`{{ .Kind }} {{ .Label | name }} {{ .Path | tilde | parent }} {{ .Meta.custom }}{{ .Meta.nope }}`, TemplateData(c))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if want := "folder shep ~/Proyectos value"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}
