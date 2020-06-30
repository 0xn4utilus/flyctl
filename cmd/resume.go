package cmd

import (
	"fmt"
	"os"

	"github.com/superfly/flyctl/cmdctx"

	"github.com/spf13/cobra"
	"github.com/superfly/flyctl/docstrings"
)

//TODO: Move all output to status styled begin/done updates

func newResumeCommand() *Command {

	resumeStrings := docstrings.Get("resume")
	resumeCmd := BuildCommand(nil, runResume, resumeStrings.Usage, resumeStrings.Short, resumeStrings.Long, os.Stdout, requireSession, requireAppNameAsArg)
	resumeCmd.Args = cobra.RangeArgs(0, 1)
	return resumeCmd
}

func runResume(ctx *cmdctx.CmdContext) error {
	app, err := ctx.Client.API().ResumeApp(ctx.AppName)
	if err != nil {
		return err
	}

	app, err = ctx.Client.API().GetApp(ctx.AppName)

	fmt.Printf("%s is now %s\n", app.Name, app.Status)

	return nil
}
