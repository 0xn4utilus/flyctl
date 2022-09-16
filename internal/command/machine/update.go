package machine

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/iostreams"

	"github.com/superfly/flyctl/flaps"
	"github.com/superfly/flyctl/internal/app"
	"github.com/superfly/flyctl/internal/command"
	"github.com/superfly/flyctl/internal/flag"
)

func newUpdate() *cobra.Command {
	const (
		short = "Update a machine"
		long  = short + "\n"

		usage = "update [machine_id]"
	)

	cmd := command.New(usage, short, long, runUpdate,
		command.RequireSession,
		command.LoadAppNameIfPresent,
	)

	flag.Add(
		cmd,
		sharedFlags,
	)

	cmd.Args = cobra.ExactArgs(1)

	return cmd
}

func runUpdate(ctx context.Context) (err error) {
	var (
		appName  = app.NameFromContext(ctx)
		io       = iostreams.FromContext(ctx)
		colorize = io.ColorScheme()
	)

	machineID := flag.FirstArg(ctx)

	app, err := appFromMachineOrName(ctx, machineID, appName)
	if err != nil {
		return err
	}

	flapsClient, err := flaps.New(ctx, app)
	if err != nil {
		return fmt.Errorf("could not make API client: %w", err)
	}

	machine, err := flapsClient.Get(ctx, machineID)
	if err != nil {
		return err
	}

	prevInstanceID := machine.InstanceID

	fmt.Fprintf(io.Out, "Machine %s was found and is currently in a %s state, attempting to update...\n", machineID, machine.State)

	input := api.LaunchMachineInput{
		ID:     machine.ID,
		AppID:  app.Name,
		Name:   machine.Name,
		Region: machine.Region,
	}

	machineConf := *machine.Config

	machineConf, err = determineMachineConfig(ctx, machineConf, app, machine.Config.Image)

	if err != nil {
		return
	}

	input.Config = &machineConf

	machine, err = flapsClient.Update(ctx, input, "")

	if err != nil {
		return err
	}

	waitForAction := "start"
	if machine.Config.Schedule != "" {
		waitForAction = "stop"
	}

	out := io.Out
	fmt.Fprintln(out, colorize.Yellow(fmt.Sprintf("Machine %s has been updated\n", machine.ID)))
	fmt.Fprintf(out, "Instance ID has been updated:\n")
	fmt.Fprintf(out, "%s -> %s\n\n", prevInstanceID, machine.InstanceID)
	fmt.Fprintf(out, "Image: %s\n", machine.Config.Image)
	fmt.Fprintf(out, "State: %s\n\n", machine.State)

	fmt.Fprintf(out, "Monitor machine status here:\nhttps://fly.io/apps/%s/machines/%s\n", app.Name, machine.ID)

	// wait for machine to be started
	if err := WaitForStartOrStop(ctx, flapsClient, machine, waitForAction, time.Minute*5); err != nil {
		return err
	}

	return nil
}
