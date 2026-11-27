// This file checks the running-agent predicate case by case.
package hc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRoleRunning checks the predicate that tells a running agent from an
// idle shell, with the user's login shell set to /bin/custom.
func TestRoleRunning(t *testing.T) {
	for _, test := range []struct {
		name  string
		state string
		argv  []string
		want  bool
	}{
		{name: "no foreground reported", state: StateLive, argv: nil, want: true},
		{name: "foreground without argv", state: StateLive, argv: []string{}, want: true},
		{name: "foreground with an empty program", state: StateLive, argv: []string{""}, want: true},
		{name: "finished terminal", state: StateFinished, argv: []string{"codex"}, want: false},
		{name: "agent", state: StateLive, argv: []string{"/usr/bin/claude"}, want: true},
		{name: "shell", state: StateLive, argv: []string{"/bin/zsh", "-l"}, want: false},
		{name: "login shell", state: StateLive, argv: []string{"-zsh", "-li"}, want: false},
		{name: "user's own shell", state: StateLive, argv: []string{"/bin/custom"}, want: false},
		{name: "shell wrapping a command", state: StateLive, argv: []string{"zsh", "-lic", "exec codex"}, want: true},
		{name: "shell running a command", state: StateLive, argv: []string{"bash", "-c", "codex"}, want: true},
		{name: "command flag in second place", state: StateLive, argv: []string{"sh", "-l", "-c"}, want: true},
		{name: "command flag in third place", state: StateLive, argv: []string{"sh", "-l", "-i", "-c"}, want: false},
		{name: "long flag", state: StateLive, argv: []string{"sh", "--command"}, want: false},
		{name: "argument that is not a flag", state: StateLive, argv: []string{"sh", "abc"}, want: false},
		{name: "xonsh", state: StateLive, argv: []string{"xonsh"}, want: false},
		{name: "elvish", state: StateLive, argv: []string{"elvish"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := Terminal{State: test.state}
			if test.argv != nil {
				terminal.Foreground = &Foreground{Argv: test.argv}
			}
			assert.Equal(t, test.want, RoleRunning(terminal, "/bin/custom"))
		})
	}

	t.Run("no login shell known", func(t *testing.T) {
		custom := Terminal{State: StateLive, Foreground: &Foreground{Argv: []string{"/bin/custom"}}}
		assert.True(t, RoleRunning(custom, ""), "an unknown program is not an idle shell")
		zsh := Terminal{State: StateLive, Foreground: &Foreground{Argv: []string{"zsh"}}}
		assert.False(t, RoleRunning(zsh, ""))
	})
}
