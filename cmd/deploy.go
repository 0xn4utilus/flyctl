package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/briandowns/spinner"
	"github.com/dustin/go-humanize"
	"github.com/logrusorgru/aurora"
	"github.com/mattn/go-isatty"
	"github.com/morikuni/aec"
	"github.com/segmentio/textio"
	"github.com/spf13/cobra"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/cmd/presenters"
	"github.com/superfly/flyctl/docker"
	"github.com/superfly/flyctl/docstrings"
	"github.com/superfly/flyctl/internal/builds"
	"github.com/superfly/flyctl/internal/deployment"
	"github.com/superfly/flyctl/terminal"
)

func newDeployCommand() *Command {
	deployStrings := docstrings.Get("deploy")
	cmd := BuildCommand(nil, runDeploy, deployStrings.Usage, deployStrings.Short, deployStrings.Long, os.Stdout, workingDirectoryFromArg(0), requireSession, requireAppName)
	cmd.AddStringFlag(StringFlagOpts{
		Name:        "image",
		Shorthand:   "i",
		Description: "Image tag or id to deploy",
	})
	cmd.AddBoolFlag(BoolFlagOpts{
		Name:        "detach",
		Description: "Return immediately instead of monitoring deployment progress",
	})
	cmd.AddBoolFlag(BoolFlagOpts{
		Name: "squash",
	})
	cmd.AddBoolFlag(BoolFlagOpts{
		Name:   "build-only",
		Hidden: true,
	})
	cmd.AddBoolFlag(BoolFlagOpts{
		Name:        "remote-only",
		Description: "Perform builds remotely without using the local docker daemon",
	})
	cmd.AddStringFlag(StringFlagOpts{
		Name:        "strategy",
		Description: "The strategy for replacing running instances. Options are canary, rolling, or immediate. Default is canary",
	})
	cmd.AddStringFlag(StringFlagOpts{
		Name:        "dockerfile",
		Description: "Path to a Dockerfile. Defaults to Dockerfile in the working directory.",
	})
	cmd.AddStringSliceFlag(StringSliceFlagOpts{
		Name:        "build-arg",
		Description: "Set of build time variables in the form of NAME=VALUE pairs. Can be specified multiple times.",
	})

	cmd.Command.Args = cobra.MaximumNArgs(1)

	return cmd
}

func runDeploy(cc *CmdContext) error {
	ctx := createCancellableContext()
	op, err := docker.NewDeployOperation(ctx, cc.AppName, cc.AppConfig, cc.Client.API(), cc.Out, cc.Config.GetBool("squash"), cc.Config.GetBool("remote-only"))
	if err != nil {
		return err
	}

	parsedCfg, err := op.ValidateConfig()
	if err != nil {
		return err
	}

	if parsedCfg.Valid {
		printAppConfigServices("  ", *parsedCfg)
	}

	var strategy = docker.DefaultDeploymentStrategy
	if val, _ := cc.Config.GetString("strategy"); val != "" {
		strategy, err = docker.ParseDeploymentStrategy(val)
		if err != nil {
			return err
		}
	}

	if imageRef, _ := cc.Config.GetString("image"); imageRef != "" {
		release, err := op.DeployImage(imageRef, strategy)
		if err != nil {
			return err
		}
		return renderRelease(ctx, cc, release)
	}

	buildArgs := map[string]string{}
	for _, arg := range cc.Config.GetStringSlice("build-arg") {
		parts := strings.Split(arg, "=")
		if len(parts) != 2 {
			return fmt.Errorf("Invalid build-arg '%s': must be in the format NAME=VALUE", arg)
		}
		buildArgs[parts[0]] = parts[1]
	}

	var dockerfilePath string

	if dockerfile, _ := cc.Config.GetString("dockerfile"); dockerfile != "" {
		dockerfilePath = dockerfile
	}

	if dockerfilePath == "" {
		dockerfilePath = docker.ResolveDockerfile(cc.WorkingDir)
	}

	if dockerfilePath == "" && !cc.AppConfig.HasBuilder() {
		return docker.ErrNoDockerfile
	}

	if cc.AppConfig.HasBuilder() {
		if dockerfilePath != "" {
			terminal.Warn("Project contains both a Dockerfile and buildpacks, using buildpacks")
		}
	}

	fmt.Printf("Deploy source directory '%s'\n", cc.WorkingDir)

	var image docker.Image

	if op.DockerAvailable() {
		fmt.Println("Docker daemon available, performing local build...")

		if cc.AppConfig.HasBuilder() {
			fmt.Println("Building with buildpacks")
			img, err := op.BuildWithPack(cc.WorkingDir, cc.AppConfig, buildArgs)
			if err != nil {
				return err
			}
			image = *img
		} else {
			fmt.Println("Building Dockerfile")

			img, err := op.BuildWithDocker(cc.WorkingDir, cc.AppConfig, dockerfilePath, buildArgs)
			if err != nil {
				return err
			}
			image = *img
		}

		fmt.Printf("Image: %+v\n", image.Tag)
		fmt.Println(aurora.Bold(fmt.Sprintf("Image size: %s", humanize.Bytes(uint64(image.Size)))))

		if err := op.PushImage(image); err != nil {
			return err
		}

		if cc.Config.GetBool("build-only") {
			fmt.Printf("Image: %s\n", image.Tag)

			return nil
		}

	} else {
		fmt.Println("Docker daemon unavailable, performing remote build...")

		build, err := op.StartRemoteBuild(cc.WorkingDir, cc.AppConfig, dockerfilePath, buildArgs)
		if err != nil {
			return err
		}

		s := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
		s.Writer = os.Stderr
		s.Prefix = "Building "
		s.Start()

		buildMonitor := builds.NewBuildMonitor(build.ID, cc.Client.API())
		for line := range buildMonitor.Logs(ctx) {
			s.Stop()
			fmt.Println(line)
			s.Start()
		}

		s.FinalMSG = fmt.Sprintf("Build complete - %s\n", buildMonitor.Status())
		s.Stop()

		if err := buildMonitor.Err(); err != nil {
			return err
		}

		build = buildMonitor.Build()
		image = docker.Image{
			Tag: build.Image,
		}
	}

	if err := op.OptimizeImage(image); err != nil {
		return err
	}

	release, err := op.Deploy(image, strategy)
	if err != nil {
		return err
	}

	op.CleanDeploymentTags()

	return renderRelease(ctx, cc, release)
}

func renderRelease(ctx context.Context, cc *CmdContext, release *api.Release) error {
	fmt.Printf("Release v%d created\n", release.Version)

	if strings.ToLower(release.DeploymentStrategy) == string(docker.ImmediateDeploymentStrategy) {
		return nil
	}

	return watchDeployment(ctx, cc)
}

func watchDeployment(ctx context.Context, cc *CmdContext) error {
	if cc.Config.GetBool("detach") {
		return nil
	}

	fmt.Println(aurora.Blue("==>"), "Monitoring Deployment")
	fmt.Println(aurora.Faint("You can detach the terminal anytime without stopping the deployment"))

	interactive := isatty.IsTerminal(os.Stdout.Fd())

	monitor := deployment.NewDeploymentMonitor(cc.Client.API(), cc.AppName)
	monitor.DeploymentStarted = func(idx int, d *api.DeploymentStatus) error {
		if idx > 0 {
			fmt.Fprintln(cc.Out)
		}
		fmt.Fprintln(cc.Out, presenters.FormatDeploymentSummary(d))
		return nil
	}
	monitor.DeploymentUpdated = func(d *api.DeploymentStatus, updatedAllocs []*api.AllocationStatus) error {
		if interactive {
			fmt.Fprint(cc.Out, aec.Up(1))
			fmt.Fprint(cc.Out, aec.EraseLine(aec.EraseModes.All))
			fmt.Fprintln(cc.Out, presenters.FormatDeploymemntAllocSummary(d))
		} else {
			for _, alloc := range updatedAllocs {
				fmt.Fprintln(cc.Out, presenters.FormatAllocSummary(alloc))
			}
		}
		return nil
	}
	monitor.DeploymentFailed = func(d *api.DeploymentStatus, failedAllocs []*api.AllocationStatus) error {
		fmt.Fprintf(cc.Out, "v%d %s - %s\n", d.Version, d.Status, d.Description)

		if len(failedAllocs) > 0 {
			fmt.Fprintln(cc.Out)
			fmt.Fprintln(cc.Out, "Failed Allocations")

			x := make(chan *api.AllocationStatus)
			var wg sync.WaitGroup
			wg.Add(len(failedAllocs))

			for _, a := range failedAllocs {
				a := a
				go func() {
					defer wg.Done()
					alloc, err := cc.Client.API().GetAllocationStatus(cc.AppName, a.ID, 20)
					if err != nil {
						fmt.Println("Error fetching alloc", a.ID, err)
						return
					}
					x <- alloc
				}()
			}

			go func() {
				wg.Wait()
				close(x)
			}()

			p := textio.NewPrefixWriter(cc.Out, "    ")

			count := 0
			for alloc := range x {
				count++
				fmt.Fprintf(cc.Out, "\n  Failure #%d\n", count)
				err := cc.RenderViewW(p,
					PresenterOption{
						Title: "Allocation",
						Presentable: &presenters.Allocations{
							Allocations: []*api.AllocationStatus{alloc},
						},
						Vertical: true,
					},
					PresenterOption{
						Title: "Recent Events",
						Presentable: &presenters.AllocationEvents{
							Events: alloc.Events,
						},
					},
				)
				if err != nil {
					return err
				}

				fmt.Fprintln(p, aurora.Bold("Reccent Logs"))
				logPresenter := presenters.LogPresenter{HideAllocID: true, HideRegion: true, RemoveNewlines: true}
				logPresenter.FPrint(p, alloc.RecentLogs)
				p.Flush()
			}

		}
		return nil
	}
	monitor.DeploymentSucceeded = func(d *api.DeploymentStatus) error {
		fmt.Fprintf(cc.Out, "v%d deployed successfully\n", d.Version)
		return nil
	}

	monitor.Start(ctx)

	if err := monitor.Error(); err != nil {
		return err
	}

	if !monitor.Success() {
		return ErrAbort
	}

	return nil
}
