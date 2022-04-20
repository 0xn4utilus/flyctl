package machine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/internal/cli/internal/app"
	"github.com/superfly/flyctl/internal/cli/internal/command"
	"github.com/superfly/flyctl/internal/client"
	"github.com/superfly/flyctl/internal/flag"
	"github.com/superfly/flyctl/pkg/flaps"
	"github.com/superfly/flyctl/pkg/iostreams"
)

func newList() *cobra.Command {
	const (
		short = "List Fly machines"
		long  = short + "\n"

		usage = "list"
	)

	cmd := command.New(usage, short, long, runMachineList,
		command.RequireSession,
		command.LoadAppNameIfPresent,
	)

	cmd.Args = cobra.NoArgs

	flag.Add(
		cmd,
		flag.App(),
		flag.AppConfig(),
		flag.Bool{
			Name:        "all",
			Description: "Show machines in all states",
		},
		flag.String{
			Name:        "state",
			Description: "List machines in a specific state.",
		},
		flag.Bool{
			Name:        "quiet",
			Shorthand:   "q",
			Description: "Only list machine ids",
		},
	)

	return cmd
}

func runMachineList(ctx context.Context) (err error) {
	var (
		appName = app.NameFromContext(ctx)
		client  = client.FromContext(ctx).API()
		io      = iostreams.FromContext(ctx)
	)

	if appName == "" {
		return fmt.Errorf("app is not found")
	}
	app, err := client.GetApp(ctx, appName)
	if err != nil {
		return err
	}
	flapsClient, err := flaps.New(ctx, app)
	if err != nil {
		return fmt.Errorf("list of machines could not be retrieved: %w", err)
	}

	machines, err := flapsClient.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("machines could not be retrieved")
	}

	var listOfMachines []api.V1Machine
	if err = json.Unmarshal(machines, listOfMachines); err != nil {
		return fmt.Errorf("list of machines could not be retrieved")
	}

	for _, machine := range listOfMachines {
		fmt.Fprintf(io.Out, "Success! A machine has been retrieved\n")
		fmt.Fprintf(io.Out, " Machine ID: %s\n", machine.ID)
		fmt.Fprintf(io.Out, " Instance ID: %s\n", machine.InstanceID)
		fmt.Fprintf(io.Out, " State: %s\n", machine.State)
	}

	return nil
}
