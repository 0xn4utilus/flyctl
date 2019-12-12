package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/logrusorgru/aurora"
	"github.com/spf13/cobra"
	"github.com/superfly/flyctl/flyctl"
	"github.com/superfly/flyctl/helpers"
)

func newConfigCommand() *Command {
	cmd := &Command{
		Command: &cobra.Command{
			Use:   "config",
			Short: "manage app configuration",
		},
	}

	BuildCommand(cmd, runViewConfig, "show", "view an app's configuration", os.Stdout, true, requireAppName)
	BuildCommand(cmd, runPullConfig, "pull", "update an app config file", os.Stdout, true, requireAppName)
	BuildCommand(cmd, runValidateConfig, "validate", "validate an app config file", os.Stdout, true, requireAppName)

	return cmd
}

func runViewConfig(ctx *CmdContext) error {
	cfg, err := ctx.FlyClient.GetConfig(ctx.AppName)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.Encode(cfg.Definition)

	return nil
}

func runPullConfig(ctx *CmdContext) error {
	if ctx.AppConfig == nil {
		ctx.AppConfig = flyctl.NewAppConfig()
	}
	ctx.AppConfig.AppName = ctx.AppName

	serverCfg, err := ctx.FlyClient.GetConfig(ctx.AppName)
	if err != nil {
		return err
	}
	ctx.AppConfig.Definition = serverCfg.Definition

	return writeAppConfig(ctx.ConfigFile, ctx.AppConfig)
}

func runValidateConfig(ctx *CmdContext) error {
	if ctx.AppConfig == nil {
		return errors.New("App config file not found")
	}

	fmt.Println("Validating", ctx.ConfigFile)

	serverCfg, err := ctx.FlyClient.ParseConfig(ctx.AppName, ctx.AppConfig.Definition)
	if err != nil {
		return err
	}

	if serverCfg.Valid {
		fmt.Println(aurora.Green("✓").String(), "Configuration is valid")
		return nil
	}

	fmt.Println()

	for _, error := range serverCfg.Errors {
		fmt.Println("   ", aurora.Red("✘").String(), error)
	}
	fmt.Println()

	return errors.New("App configuration is not valid")
}

func writeAppConfig(path string, appConfig *flyctl.AppConfig) error {
	if !confirmFileOverwrite(path) {
		return nil
	}

	if err := appConfig.WriteToFile(path); err != nil {
		return err
	}

	fmt.Println("Wrote config file", helpers.PathRelativeToCWD(path))

	return nil
}
