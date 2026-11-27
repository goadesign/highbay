// This file tells a terminal running an agent from one sitting at an idle
// shell prompt.

package hc

import (
	"path/filepath"
	"strings"
)

// idleShells are the command interpreters that, sitting in a terminal's
// foreground without a command to run, mean no agent is running there.
var idleShells = []string{"sh", "bash", "zsh", "fish", "dash", "ksh", "csh", "tcsh", "nu", "pwsh", "xonsh", "elvish"}

// RoleRunning reports whether a terminal is running an agent rather than
// sitting at an idle shell prompt. shell is the user's login shell, normally
// the SHELL environment variable.
//
// A live terminal for which the core reports no foreground program counts as
// running; only a reported foreground that is an idle shell makes it not
// running. An idle shell has the base name of argv[0] (leading dashes
// removed) among idleShells or equal to the base name of shell, and neither
// argv[1] nor argv[2] is -c or a single-dash flag group containing c (such as
// -lic), which would mean the shell is running a command.
func RoleRunning(t Terminal, shell string) bool {
	if t.State != StateLive {
		return false
	}
	if t.Foreground == nil {
		return true
	}
	argv := t.Foreground.Argv
	if len(argv) == 0 || argv[0] == "" {
		return true
	}
	name := shellName(argv[0])
	idleShell := shell != "" && name == shellName(shell)
	for _, candidate := range idleShells {
		if name == candidate {
			idleShell = true
		}
	}
	if !idleShell {
		return true
	}
	for _, arg := range argv[1:min(len(argv), 3)] {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "c") {
			return true
		}
	}
	return false
}

// shellName reduces a program path or a login shell's argv[0] ("-zsh") to its
// bare name.
func shellName(program string) string {
	return strings.TrimLeft(filepath.Base(strings.TrimLeft(program, "-")), "-")
}
