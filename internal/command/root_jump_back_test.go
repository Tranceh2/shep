package command

import "testing"

// TestRootCmd_RegistersJumpBackPreservingBaseline proves the Unit 3a root
// registration is additive: jump-back is mounted as a public command and every
// pre-existing command stays registered.
func TestRootCmd_RegistersJumpBackPreservingBaseline(t *testing.T) {
	root := New().rootCmd()

	registered := make(map[string]bool)
	hidden := make(map[string]bool)
	for _, sub := range root.Commands() {
		registered[sub.Name()] = true
		hidden[sub.Name()] = sub.Hidden
	}

	if !registered["jump-back"] {
		t.Fatal("root command tree is missing jump-back")
	}
	if hidden["jump-back"] {
		t.Fatal("jump-back must be a public command")
	}
	for _, name := range []string{"init", "list", "open", "preview", "doctor", "ranking"} {
		if !registered[name] {
			t.Fatalf("root registration dropped the existing %q command", name)
		}
	}
}
