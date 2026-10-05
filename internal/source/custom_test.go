package source_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

func TestCustomSourceRejectsReservedMetadata(t *testing.T) {
	t.Parallel()
	keys := []string{
		"command", "template", "close_on_exit", "group", "group_sources", "group_template",
		"parent_template", "workspace_id", "tab_id", "pane_id", "custom_source", "custom_source_id",
		"active_tab_id", "agent_status", "branch", "default", "entry_id", "head", "is_worktree",
		"main_worktree", "repo", "running", "session_dir", "session_name", "socket_path",
		"tab_label", "tab_number", "tab_panes", "workspace_label", "workspace_tabs",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			payload := []byte(`[{"label":"row","meta":{"` + key + `":"attacker"}}]`)
			_, err := source.ParseCustomSourceJSON("test", payload)
			if err == nil {
				t.Fatalf("meta key %q was accepted", key)
			}
			want := `row 0: meta key "` + key + `" is reserved`
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want substring %q", err, want)
			}
		})
	}
}

func TestCustomSourceTypedReservedFieldsRemainUsable(t *testing.T) {
	t.Parallel()
	candidates, err := source.ParseCustomSourceJSON("test", []byte(`[{"id":"id","label":"row","command":"run","template":"tpl","close_on_exit":true,"meta":{"context":"safe"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got := candidates[0].Meta; got["command"] != "run" || got["template"] != "tpl" || got["close_on_exit"] != "true" || got["custom_source"] != "true" || got["custom_source_id"] != "id" || got["context"] != "safe" {
		t.Fatalf("typed fields/meta = %#v", got)
	}
}

func TestCustomSourceAliasesRejectControlCharacters(t *testing.T) {
	candidates, err := source.ParseCustomSourceJSONWithAliases("test", []byte(`[{"label":"row","aliases":["safe","bad\nvalue"]}]`), []string{"shared\tbad"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidates[0].Aliases, []string{"safe"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
}

func TestCustomSourceAliasesDoNotChangeIdentity(t *testing.T) {
	cases := []struct {
		name string
		one  string
		two  string
	}{
		{name: "explicit id", one: `[{"id":"42","label":"PR 42","aliases":["review"]}]`, two: `[{"id":"42","label":"PR 42","aliases":["different"]}]`},
		{name: "fallback identity", one: `[{"label":"PR 42","command":"gh pr view 42","aliases":["review"]}]`, two: `[{"label":"PR 42","command":"gh pr view 42","aliases":["different"]}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			one, err := source.ParseCustomSourceJSON("prs", []byte(tc.one))
			if err != nil {
				t.Fatal(err)
			}
			two, err := source.ParseCustomSourceJSON("prs", []byte(tc.two))
			if err != nil {
				t.Fatal(err)
			}
			if ranking.Identity(one[0]) != ranking.Identity(two[0]) {
				t.Fatal("custom_source identity changed when aliases changed")
			}
		})
	}
}

func TestCustomSourceCommandOnlyIdentityIsStableAndOpaque(t *testing.T) {
	payload := []byte(`[{"id":"42","label":"PR 42","command":"gh pr view 42"}]`)
	first, err := source.ParseCustomSourceJSON("prs", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.ParseCustomSourceJSON("prs", payload)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ranking.Identity(first[0]), ranking.Identity(second[0]); got != want {
		t.Fatalf("identity changed across parses: %q != %q", got, want)
	}
	if ranking.Resource(first[0]) != "" {
		t.Fatal("command-only custom_source unexpectedly has a resource key")
	}
	if key := ranking.PinKey(first[0]); key == "" || !hasOpaqueKeyPrefix(key) {
		t.Fatalf("command-only custom_source key is not opaque: %q", key)
	}
}

func TestCustomSourceCommandOnlyIdentityFallbackIsDeterministic(t *testing.T) {
	one, err := source.ParseCustomSourceJSON("prs", []byte(`[{"label":"PR 42","command":"gh pr view 42"}]`))
	if err != nil {
		t.Fatal(err)
	}
	two, err := source.ParseCustomSourceJSON("prs", []byte(`[{"label":"PR 42","command":"gh pr view 42"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if ranking.Identity(one[0]) != ranking.Identity(two[0]) {
		t.Fatal("fallback custom_source identity is not deterministic")
	}
}

func TestCustomSourceSharedAndRowAliasesCombine(t *testing.T) {
	candidates, err := source.ParseCustomSourceJSON("kube-contexts", []byte(`[{"label":"prod","aliases":[" row ","K8S"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidates[0].Aliases, []string{"row", "K8S"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("row aliases = %v, want %v", got, want)
	}
	candidates, err = source.ParseCustomSourceJSONWithAliases("kube-contexts", []byte(`[{"label":"prod","aliases":["row","K8S"]}]`), []string{"k8s", " shared ", "K8S"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := candidates[0].Aliases, []string{"k8s", "shared", "row"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("combined aliases = %v, want %v", got, want)
	}
}

func TestCustomSourceRejectsConflictingDuplicateIDs(t *testing.T) {
	_, err := source.ParseCustomSourceJSON("kube-contexts", []byte(`[
		{"id":"cluster-a","label":"cluster-a","command":"kubectl --context direct"},
		{"id":"cluster-a","label":"cluster-a","command":"kubectl --context connect"}
	]`))
	if err == nil {
		t.Fatal("expected conflicting duplicate custom_source ids to be rejected")
	}
	if got := err.Error(); got != `row 1: custom source id "cluster-a" has conflicting commands` {
		t.Fatalf("error = %q, want deterministic duplicate-id error", got)
	}
}

func TestCustomSourceAllowsIdenticalDuplicateIDs(t *testing.T) {
	candidates, err := source.ParseCustomSourceJSON("kube-contexts", []byte(`[
		{"id":"cluster-a","label":"cluster-a","command":"kubectl --context direct"},
		{"id":"cluster-a","label":"cluster-a","command":"kubectl --context direct"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("parser must preserve rows for resolver dedup, got %d", len(candidates))
	}
	if ranking.Identity(candidates[0]) != ranking.Identity(candidates[1]) {
		t.Fatal("identical explicit ids must have identical identities")
	}
}

func hasOpaqueKeyPrefix(key string) bool {
	return len(key) > len("v1:exact:") && key[:len("v1:")] == "v1:"
}
