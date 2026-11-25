// This file reads an existing local project's management status.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	genprojectserver "github.com/goadesign/highbay/gen/http/project/server"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/project"
)

// status reports whether the current project's server is running and, if so,
// shows its active checkpoint and team. It never prepares project storage.
func (runtime projectRuntime) status(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print project status as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("status takes no arguments")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := runtime.readProjectStatus(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		result := struct {
			Running bool `json:"running"`
			*genprojectserver.StatusResponseBody
		}{Running: status != nil}
		if status != nil {
			result.StatusResponseBody = genprojectserver.NewStatusResponseBody(status)
		}
		return json.NewEncoder(stdout).Encode(result)
	}
	if status == nil {
		_, err := fmt.Fprintln(stdout, "Highbay is stopped.")
		return err
	}
	return writeProjectStatus(stdout, status)
}

// readProjectStatus confirms the existing writer lock and server identity
// before asking the management endpoint to project its state.
func (runtime projectRuntime) readProjectStatus(ctx context.Context) (*genproject.ProjectStatus, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := runtime.operatorRootCandidate(cwd)
	if err != nil {
		return nil, err
	}
	if err := validateOperatorPrivateDir(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read operator storage root %s: %w", root, err)
	}
	state, err := project.Discover(root, cwd)
	if err != nil {
		return nil, err
	}
	if state.Record.ID == "" {
		return nil, nil
	}
	inUse, err := controlplane.CardStoreInUse(state.CardsDir())
	if err != nil || !inUse {
		return nil, err
	}
	client := projectClient(state)
	if _, err := confirmProject(ctx, state, client); err != nil {
		return nil, fmt.Errorf("%s: %w", projectHolder(state), err)
	}
	return client.Status(ctx)
}

// writeProjectStatus shows the management projection in a compact terminal view.
func writeProjectStatus(stdout io.Writer, status *genproject.ProjectStatus) error {
	var out strings.Builder
	fmt.Fprintf(&out, "%s (%s)\nStage %s\nHerdr %s\n", status.ProjectName, status.Directory, status.Stage, status.HerdrMode)
	if status.ChangeID != "" {
		fmt.Fprintf(&out, "Change %s (%s)\n", status.ChangeTitle, status.ChangeID)
	}
	if status.Team != "" {
		fmt.Fprintf(&out, "Team %s\n", status.Team)
	}
	if status.Attention != "" {
		fmt.Fprintf(&out, "Attention %s\n", status.Attention)
	}
	for _, role := range status.Roles {
		fmt.Fprintf(&out, "  %s (%s) %s\n", role.Role, role.Handle, role.State)
	}
	_, err := io.WriteString(stdout, printableTerminalText(out.String()))
	return err
}
