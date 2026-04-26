package cliutil

import (
	"context"
	"fmt"
	"time"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// WaitAgentIdle polls until the agent run reaches idle state, returns error on
// error/stopped states. Prints progress to stdout.
func WaitAgentIdle(ctx context.Context, client ariclient.Client, wsName, agName string) error {
	fmt.Printf("Waiting for agent %q/%q to be idle...\n", wsName, agName)
	for {
		time.Sleep(500 * time.Millisecond)
		var ar pkgariapi.AgentRun
		if err := client.Get(ctx, pkgariapi.ObjectKey{Workspace: wsName, Name: agName}, &ar); err != nil {
			return fmt.Errorf("agentrun/get %q: %w", agName, err)
		}
		switch ar.Status.Status {
		case "idle":
			fmt.Printf("Agent %q/%q is idle\n", wsName, agName)
			return nil
		case "error":
			return fmt.Errorf("agent %q/%q entered error state: %s", wsName, agName, ar.Status.ErrorMessage)
		case "stopped":
			return fmt.Errorf("agent %q/%q stopped unexpectedly", wsName, agName)
		}
	}
}
