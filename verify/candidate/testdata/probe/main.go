// Command probe is a stand-in candidate the package's tests run through Exec,
// Run and Output. Each argument names one action; the probe attempts it and
// prints one JSON object per line reporting whether it succeeded.
//
// Actions: write:<path>, env:<name>, orphan:<pidfile> (starts /bin/sleep 300
// sharing the probe's standard output and standard error, writes its PID to
// pidfile, and does not wait for it), stderr:<text> (prints text on standard
// error), exit:<code> (exits with code at once).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// result reports one attempted action.
type result struct {
	// Action is the argument as given.
	Action string `json:"action"`
	// Allowed reports whether the action succeeded.
	Allowed bool `json:"allowed"`
	// Detail is the value read or the error returned.
	Detail string `json:"detail"`
}

func main() {
	enc := json.NewEncoder(os.Stdout)
	for _, arg := range os.Args[1:] {
		kind, target, _ := strings.Cut(arg, ":")
		r := result{Action: arg}
		err := attempt(kind, target, &r)
		r.Allowed = err == nil
		if err != nil {
			r.Detail = err.Error()
		}
		if encErr := enc.Encode(r); encErr != nil {
			fmt.Fprintln(os.Stderr, encErr)
			os.Exit(1)
		}
	}
}

// attempt performs one action and records what it observed.
func attempt(kind, target string, r *result) error {
	switch kind {
	case "write":
		return os.WriteFile(target, []byte("probe"), 0o600)
	case "env":
		v, ok := os.LookupEnv(target)
		if !ok {
			return fmt.Errorf("%s is not set", target)
		}
		r.Detail = v
		return nil
	case "orphan":
		child := exec.Command("/bin/sleep", "300")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			return err
		}
		r.Detail = strconv.Itoa(child.Process.Pid)
		return os.WriteFile(target, []byte(r.Detail), 0o600)
	case "stderr":
		_, err := fmt.Fprintln(os.Stderr, target)
		return err
	case "exit":
		code, err := strconv.Atoi(target)
		if err != nil {
			return err
		}
		os.Exit(code)
		return nil
	default:
		return fmt.Errorf("unknown action %q", kind)
	}
}
