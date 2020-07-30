package cmd

import (
	"fmt"
	"os"
	"path"
	"strconv"

	"github.com/superfly/flyctl/cmdctx"

	"github.com/AlecAivazis/survey/v2"
	"github.com/spf13/cobra"
	"github.com/superfly/flyctl/cmd/presenters"
	"github.com/superfly/flyctl/docstrings"
	"github.com/superfly/flyctl/flyctl"
	"github.com/superfly/flyctl/helpers"
)

//TODO: Move all output to status styled begin/done updates

func newInitCommand() *Command {

	initStrings := docstrings.Get("init")

	cmd := BuildCommand(nil, runInit, initStrings.Usage, initStrings.Short, initStrings.Long, os.Stdout, requireSession)

	cmd.Args = cobra.RangeArgs(0, 1)

	// TODO: Move flag descriptions into the docStrings
	cmd.AddStringFlag(StringFlagOpts{
		Name:        "name",
		Description: "The app name to use",
	})

	cmd.AddStringFlag(StringFlagOpts{
		Name:        "org",
		Description: `The organization that will own the app`,
	})

	cmd.AddStringFlag(StringFlagOpts{
		Name:        "port",
		Shorthand:   "p",
		Description: "Internal port on application to connect to external services",
	})

	cmd.AddStringFlag(StringFlagOpts{
		Name:        "builder",
		Description: `The Cloud Native Buildpacks builder to use when deploying the app`,
	})

	cmd.AddStringFlag(StringFlagOpts{
		Name:        "import",
		Description: "Create but import all settings from the given file",
	})

	cmd.AddBoolFlag(BoolFlagOpts{
		Name:        "dockerfile",
		Description: `Use a dockerfile when deploying the app`,
		Default:     false,
	})

	return cmd
}

func runInit(commandContext *cmdctx.CmdContext) error {
	var appName = ""
	var internalPort = 0

	if len(commandContext.Args) > 0 {
		appName = commandContext.Args[0]
	}

	configPort, _ := commandContext.Config.GetString("port")

	// If ports set, validate
	if configPort != "" {
		var err error

		internalPort, err = strconv.Atoi(configPort)
		if err != nil {
			return fmt.Errorf(`-p ports must be numeric`)
		}
	}

	configfilename, err := flyctl.ResolveConfigFileFromPath(commandContext.WorkingDir)

	if helpers.FileExists(configfilename) {
		commandContext.Status("create", cmdctx.SERROR, "An existing configuration file has been found.")
		confirmation := confirm(fmt.Sprintf("Overwrite file '%s'", configfilename))
		if !confirmation {
			return nil
		}
	}

	newAppConfig := flyctl.NewAppConfig()

	name, _ := commandContext.Config.GetString("name")

	if name != "" && appName != "" {
		return fmt.Errorf(`two app names specified %s and %s. Select and specify only one`, appName, name)
	}

	if name == "" && appName != "" {
		name = appName
	}

	fmt.Println()

	if name == "" {
		prompt := &survey.Input{
			Message: "App Name (leave blank to use an auto-generated name)",
		}
		if err := survey.AskOne(prompt, &name); err != nil {
			if isInterrupt(err) {
				return nil
			}
		}
	} else {
		fmt.Printf("Selected App Name: %s\n", name)
	}

	fmt.Println()

	targetOrgSlug, _ := commandContext.Config.GetString("org")
	org, err := selectOrganization(commandContext.Client.API(), targetOrgSlug)

	switch {
	case isInterrupt(err):
		return nil
	case err != nil || org == nil:
		return fmt.Errorf("Error setting organization: %s", err)
	}

	fmt.Println()

	builder, _ := commandContext.Config.GetString("builder")
	if err != nil {
		return err
	}

	importfile, err := commandContext.Config.GetString("import")
	if err != nil {
		return err
	}

	// If we are importing, assume builders are set in the template
	if importfile == "" {
		// Otherwise get a Builder from the user while checking the dockerfile setting
		dockerfileSet := commandContext.Config.IsSet("dockerfile")
		dockerfile := commandContext.Config.GetBool("dockerfile")

		if builder == "" && !dockerfileSet {
			builder, err := selectBuildtype(commandContext)

			switch {
			case isInterrupt(err):
				return nil
			case err != nil || org == nil:
				return fmt.Errorf("Error setting builder: %s", err)
			}
			if builder != "Dockerfile" {
				newAppConfig.Build = &flyctl.Build{Builder: builder}
			} else {
				dockerfileExists := helpers.FileExists(path.Join(commandContext.WorkingDir, "Dockerfile"))
				if !dockerfileExists {
					newdf, err := os.Create(path.Join(commandContext.WorkingDir, "Dockerfile"))
					if err != nil {
						return fmt.Errorf("Error writing example Dockerfile: %s", err)
					}
					fmt.Fprintf(newdf, "FROM flyio/hellofly\n")
					newdf.Close()
				}
			}
		} else if builder != "" {
			// If the builder was set and there's not dockerfile setting, write the builder
			if !dockerfile {
				newAppConfig.Build = &flyctl.Build{Builder: builder}
			}
		}
	}

	// The creation magic happens here....
	app, err := commandContext.Client.API().CreateApp(name, org.ID)
	if err != nil {
		return err
	}

	if importfile != "" {
		fmt.Printf("Importing configuration from %s\n", importfile)

		tmpappconfig, err := flyctl.LoadAppConfig(importfile)
		if err != nil {
			return err
		}
		newAppConfig = tmpappconfig
		// And then overwrite the app name
		newAppConfig.AppName = app.Name
	} else {
		newAppConfig.AppName = app.Name
		newAppConfig.Definition = app.Config.Definition
		if configPort != "" {
			newAppConfig.SetInternalPort(internalPort)
		}
	}
	fmt.Println()

	err = commandContext.Frender(cmdctx.PresenterOption{Presentable: &presenters.AppInfo{App: *app}, HideHeader: true, Vertical: true, Title: "New app created"})
	if err != nil {
		return err
	}

	if commandContext.ConfigFile == "" {
		newCfgFile, err := flyctl.ResolveConfigFileFromPath(commandContext.WorkingDir)
		if err != nil {
			return err
		}
		commandContext.ConfigFile = newCfgFile
	}

	commandContext.AppName = app.Name
	commandContext.AppConfig = newAppConfig

	return writeAppConfig(commandContext.ConfigFile, newAppConfig)
}
