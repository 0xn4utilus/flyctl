package postgres

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/superfly/flyctl/agent"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/client"
	"github.com/superfly/flyctl/flaps"
	"github.com/superfly/flyctl/flypg"
	"github.com/superfly/flyctl/internal/app"
	"github.com/superfly/flyctl/internal/command"
	"github.com/superfly/flyctl/internal/command/machine"
	"github.com/superfly/flyctl/internal/flag"
	"github.com/superfly/flyctl/internal/spinner"
	"github.com/superfly/flyctl/iostreams"
)

func newRestart() *cobra.Command {
	const (
		short = "Restarts each member of the Postgres cluster one by one."
		long  = short + " Downtime should be minimal." + "\n"
		usage = "restart"
	)

	cmd := command.New(usage, short, long, runRestart,
		command.RequireSession,
		command.RequireAppName,
	)

	flag.Add(cmd,
		flag.App(),
		flag.AppConfig(),
		flag.Bool{
			Name:        "hard",
			Description: "Forces cluster VMs restarts",
		},
	)

	return cmd
}

func runRestart(ctx context.Context) error {
	var (
		MinPostgresHaVersion = "0.0.20"
		io                   = iostreams.FromContext(ctx)
		client               = client.FromContext(ctx).API()
		appName              = app.NameFromContext(ctx)
	)

	app, err := client.GetAppCompact(ctx, appName)
	if err != nil {
		return fmt.Errorf("get app: %w", err)
	}

	if !app.IsPostgresApp() {
		return fmt.Errorf("app %s is not a Postgres app", app.Name)
	}

	agentclient, err := agent.Establish(ctx, client)
	if err != nil {
		return fmt.Errorf("can't establish agent %w", err)
	}

	dialer, err := agentclient.Dialer(ctx, app.Organization.Slug)
	if err != nil {
		return fmt.Errorf("can't build tunnel for %s: %s", app.Organization.Slug, err)
	}
	ctx = agent.DialerWithContext(ctx, dialer)

	switch app.PlatformVersion {
	case "nomad":
		if err := hasRequiredVersionOnNomad(app, MinPostgresHaVersion, MinPostgresHaVersion); err != nil {
			return err
		}
		if flag.GetBool(ctx, "hard") {
			s := spinner.Run(io, "Restarting cluster VMs")

			allocs, err := client.GetAllocations(ctx, appName, false)
			if err != nil {
				return fmt.Errorf("get app status: %w", err)
			}
			for _, alloc := range allocs {

				if err := client.RestartAllocation(ctx, appName, alloc.ID); err != nil {
					return fmt.Errorf("failed to restart vm %s: %w", alloc.ID, err)
				}

			}
			s.StopWithMessage("Successfully restarted all cluster VMs")

			return nil
		}
		return restartNomadPG(ctx, app)
	case "machines":
		flapsClient, err := flaps.New(ctx, app)
		if err != nil {
			return fmt.Errorf("list of machines could not be retrieved: %w", err)
		}
		ctx = flaps.NewContext(ctx, flapsClient)

		members, err := flapsClient.List(ctx, "started")
		if err != nil {
			return fmt.Errorf("machines could not be retrieved %w", err)
		}
		leader, replicas, err := nodeRoles(ctx, members)
		if err != nil {
			return fmt.Errorf("can't fetch leader: %w", err)
		}
		if leader == nil {
			return fmt.Errorf("no leader found")
		}

		if err := hasRequiredVersionOnMachines(leader, MinPostgresHaVersion, MinPostgresHaVersion); err != nil {
			return err
		}
		if flag.GetBool(ctx, "hard") {
			s := spinner.Run(io, "Restarting cluster VMs")

			if len(replicas) > 0 {

				for _, replica := range replicas {
					if err := machine.Restart(ctx, replica); err != nil {
						return fmt.Errorf("failed to restart vm %s: %w", replica.ID, err)
					}
				}
			}

			// Don't perform failover if the cluster is only running a
			// single node.
			if len(members) > 1 {
				pgclient := flypg.New(app.Name, dialer)

				fmt.Fprintf(io.Out, "Performing a failover\n")
				if err := pgclient.Failover(ctx); err != nil {
					return fmt.Errorf("failed to trigger failover %w", err)
				}
			}

			pgclient := flypg.New(app.Name, dialer)

			if err := pgclient.Failover(ctx); err != nil {
				return fmt.Errorf("failed to trigger failover %w", err)
			}

			if err := machine.Restart(ctx, leader); err != nil {
				return fmt.Errorf("failed to restart vm %s: %w", leader.ID, err)
			}
			s.StopWithMessage("Successfully restarted all cluster VMs")

			return nil
		}
		return restartMachinesPG(ctx, app)
	}

	return nil
}

func restartMachinesPG(ctx context.Context, app *api.AppCompact) error {
	var (
		dialer = agent.DialerFromContext(ctx)
		io     = iostreams.FromContext(ctx)
	)

	flapsClient, err := flaps.New(ctx, app)
	if err != nil {
		return fmt.Errorf("list of machines could not be retrieved: %w", err)
	}

	machines, err := flapsClient.List(ctx, "started")
	if err != nil {
		return fmt.Errorf("machines could not be retrieved %w", err)
	}

	if len(machines) == 0 {
		return fmt.Errorf("no machines found")
	}

	leader, replicas, err := nodeRoles(ctx, machines)
	if err != nil {
		return fmt.Errorf("can't fetch leader: %w", err)
	}
	if leader == nil {
		return fmt.Errorf("no leader found")
	}

	// Acquire leases
	fmt.Fprintf(io.Out, "Attempting to acquire lease(s)\n")

	for _, machine := range machines {
		lease, err := flapsClient.GetLease(ctx, machine.ID, api.IntPointer(40))
		if err != nil {
			return fmt.Errorf("failed to obtain lease: %w", err)
		}
		machine.LeaseNonce = lease.Data.Nonce

		// Ensure lease is released on return
		defer releaseLease(ctx, flapsClient, machine)

		fmt.Fprintf(io.Out, "  Machine %s: %s\n", machine.ID, lease.Status)
	}

	if len(replicas) > 0 {
		fmt.Fprintf(io.Out, "Attempting to restart replica(s)\n")

		for _, replica := range replicas {
			fmt.Fprintf(io.Out, " Restarting %s \n", replica.ID)

			pgclient := flypg.NewFromInstance(fmt.Sprintf("[%s]", replica.PrivateIP), dialer)

			if err := pgclient.RestartNodePG(ctx); err != nil {
				return fmt.Errorf("failed to restart postgres on node: %w", err)
			}
		}
	}

	// Don't perform failover if the cluster is only running a
	// single node.
	if len(machines) > 1 {
		pgclient := flypg.New(app.Name, dialer)

		fmt.Fprintf(io.Out, "Performing a failover\n")
		if err := pgclient.Failover(ctx); err != nil {
			return fmt.Errorf("failed to trigger failover %w", err)
		}
	}

	fmt.Fprintf(io.Out, "Attempting to restart leader\n")

	pgclient := flypg.NewFromInstance(fmt.Sprintf("[%s]", leader.PrivateIP), dialer)

	if err := pgclient.RestartNodePG(ctx); err != nil {
		return fmt.Errorf("failed to restart postgres on node: %w", err)
	}

	fmt.Fprintf(io.Out, "Postgres cluster has been successfully restarted!\n")

	return nil
}

func restartNomadPG(ctx context.Context, app *api.AppCompact) (err error) {
	var (
		client = client.FromContext(ctx).API()
		io     = iostreams.FromContext(ctx)
	)

	status, err := client.GetAppStatus(ctx, app.Name, false)
	if err != nil {
		return fmt.Errorf("get app status: %w", err)
	}

	var vms []*api.AllocationStatus

	vms = append(vms, status.Allocations...)

	if len(vms) == 0 {
		return fmt.Errorf("no vms found")
	}

	agentclient, err := agent.Establish(ctx, client)
	if err != nil {
		return fmt.Errorf("can't establish agent %w", err)
	}

	dialer, err := agentclient.Dialer(ctx, app.Organization.Slug)
	if err != nil {
		return fmt.Errorf("can't build tunnel for %s: %s", app.Organization.Slug, err)
	}

	fmt.Fprintln(io.Out, "Restarting the Postgres Processs")

	for _, vm := range vms {
		fmt.Fprintf(io.Out, " Restarting %s\n", vm.ID)

		pgclient := flypg.NewFromInstance(fmt.Sprintf("[%s]", vm.PrivateIP), dialer)

		if err := pgclient.RestartNodePG(ctx); err != nil {
			return fmt.Errorf("failed to restart postgres on node: %w", err)
		}
	}
	fmt.Fprintf(io.Out, "Restart complete\n")

	return
}
