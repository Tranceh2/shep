package tmpl

// SampleHome is the home directory every sample path lives under. A
// validation engine is built with it (New(SampleHome)) so validating a
// template never depends on the real $HOME.
const SampleHome = "/home/user"

// Samples returns one representative Data value per requested kind (every
// kind, in Kinds order, when none are named), shaped like the rows real
// providers produce: the fields that apply to that kind carry realistic,
// non-empty values and the fields that do not are empty, exactly as at
// runtime. Unknown kinds are skipped. Every call returns fresh values.
func Samples(kinds ...string) []Data {
	if len(kinds) == 0 {
		kinds = Kinds()
	}
	out := make([]Data, 0, len(kinds))
	for _, kind := range kinds {
		if d, ok := sample(kind); ok {
			out = append(out, d)
		}
	}
	return out
}

func sample(kind string) (Data, bool) {
	const project = SampleHome + "/Proyectos/shep"
	const worktree = SampleHome + "/Proyectos/shep-worktrees/themes"
	switch kind {
	case KindWorkspace:
		return Data{
			Path: project, NormalizedPath: project, Label: "~/Proyectos/shep",
			Source: "herdr", Kind: kind,
			Meta: map[string]string{"workspace_id": "w1", "active_tab_id": "w1:t1"},
		}, true
	case KindConfigured:
		return Data{
			Path: SampleHome + "/work/api", NormalizedPath: SampleHome + "/work/api", Label: "api",
			Source: "workspaces", Kind: kind,
			Meta: map[string]string{"entry_id": "api", "workspace_name": "api", "command": "nvim"},
		}, true
	case KindGroup:
		return Data{
			Path: SampleHome + "/work", NormalizedPath: SampleHome + "/work", Label: "Platform",
			Source: "workspaces", Kind: kind,
			Meta: map[string]string{"entry_id": "platform", "workspace_name": "Platform", "group": "true", "group_sources": "projects,zoxide"},
		}, true
	case KindFolder:
		return Data{
			Path: SampleHome + "/Documents/notes", NormalizedPath: SampleHome + "/Documents/notes",
			Label: "~/Documents/notes", Source: "zoxide", Kind: kind,
		}, true
	case KindProject:
		return Data{
			Path: project, NormalizedPath: project, Label: "~/Proyectos/shep",
			Source: "projects", Kind: kind,
		}, true
	case KindWorktree:
		return Data{
			Path: worktree, NormalizedPath: worktree, Label: "~/Proyectos/shep-worktrees/themes",
			Source: "projects", Kind: kind,
			Branch: "feat/themes", Head: "a8083df", RepoName: "shep", IsWorktree: true,
			Meta: map[string]string{
				"is_worktree": "true", "main_worktree": "false", "branch": "feat/themes",
				"repo": "shep", "worktree_path": worktree, "head": "a8083df1c2b3d4e5f60718293a4b5c6d7e8f9012",
			},
		}, true
	case KindSession:
		return Data{
			Path: SampleHome, NormalizedPath: SampleHome, Label: "default",
			Source: "sessions", Kind: kind,
			Meta: map[string]string{
				"session_name": "default", "running": "true", "default": "true",
				"session_dir": SampleHome + "/.local/state/herdr/sessions/default",
				"socket_path": SampleHome + "/.local/state/herdr/sessions/default/herdr.sock",
			},
		}, true
	case KindAgent:
		return Data{
			Path: project, NormalizedPath: project, Label: "claude: refactor the picker",
			Source: "agents", Kind: kind,
			Agent: "claude", AgentStatus: "working", TabLabel: "agents", Workspace: "~/Proyectos/shep",
			Meta: map[string]string{
				"workspace_id": "w1", "workspace_label": "~/Proyectos/shep", "tab_id": "w1:t2",
				"tab_label": "agents", "pane_id": "w1:t2:p1", "agent": "claude",
				"agent_status": "working", "terminal_title": "claude: refactor the picker", "kind": "agent",
			},
		}, true
	case KindTab:
		return Data{
			Path: project, Label: "2 editor", Kind: kind,
			TabNumber: "2", TabLabel: "editor", Workspace: "~/Proyectos/shep",
			Meta: map[string]string{
				"workspace_id": "w1", "workspace_label": "~/Proyectos/shep", "tab_id": "w1:t1",
				"tab_number": "2", "tab_label": "editor",
			},
		}, true
	case KindPane:
		return Data{
			Path: project, Label: "nvim", Kind: kind,
			Agent: "claude", AgentStatus: "idle", TabLabel: "editor", Workspace: "~/Proyectos/shep",
			Meta: map[string]string{
				"workspace_id": "w1", "workspace_label": "~/Proyectos/shep", "tab_id": "w1:t1",
				"tab_label": "editor", "pane_id": "w1:t1:p1", "agent_status": "idle",
				"agent": "claude", "terminal_title": "nvim",
			},
		}, true
	case KindCustom:
		return Data{
			Path: project, Label: "#42 Unify templates", Source: "prs", Kind: kind, Icon: "",
			Meta: map[string]string{
				"custom_source": "true", "custom_source_id": "42",
				"url": "https://example.com/shep/pull/42", "author": "octocat",
			},
		}, true
	}
	return Data{}, false
}
