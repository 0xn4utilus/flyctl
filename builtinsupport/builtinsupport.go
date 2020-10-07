package builtinsupport

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/superfly/flyctl/cmdctx"
	"github.com/superfly/flyctl/flyctl"
)

// Arg is a simple holder for names and defaults in args
type Arg struct {
	Name        string
	Default     interface{}
	Description string
}

// Builtin - Definition of a Fly Builtin Builder
type Builtin struct {
	Name        string
	Description string
	Details     string
	Template    string
	BuiltinArgs []Arg
	argMap      map[string]Arg
}

var builtins map[string]Builtin

// GetBuiltin - Finds the Builtin by name
func GetBuiltin(commandContext *cmdctx.CmdContext, builtinname string) (*Builtin, error) {
	initBuiltins(commandContext)

	builtin, ok := builtins[builtinname]

	if !ok {
		return nil, fmt.Errorf("no builtin with %s name supported", builtinname)
	}

	return &builtin, nil
}

// ResolveArgs - Given defaults abd values return actural settings
func (b *Builtin) ResolveArgs(vars map[string]interface{}) map[string]interface{} {
	settings := make(map[string]interface{}, len(vars))

	for k, v := range vars {
		if b.BuiltinArgs != nil {
			for _, arg := range b.BuiltinArgs {
				if arg.Name == k {
					// This is good to add
					settings[k] = v
					break
				}
			}
		}
	}

	// settings now has all the values which were in Builtinargs, but no others

	// Now scan builtinargs for any value not set and copy the default over
	for _, arg := range b.BuiltinArgs {
		_, found := settings[arg.Name]
		if !found {
			// This is good to set to default
			settings[arg.Name] = arg.Default
		}
	}

	return settings
}

// GetVDockerfile - given an map of variables, get the definition and populate it
func (b *Builtin) GetVDockerfile(vars map[string]interface{}) (string, error) {
	template, err := template.New("builtin").Parse(b.Template)

	if err != nil {
		return "", err
	}

	// Now the create the proper vars from
	// If it's set in the vars map, set it in the settings map

	settings := b.ResolveArgs(vars)

	result := strings.Builder{}

	err = template.Execute(&result, settings)
	if err != nil {
		return "", err
	}

	return result.String(), nil
}

// GetArg - Gets the Arg structure for a named arg
func (b *Builtin) GetArg(name string) Arg {
	if len(b.argMap) != len(b.BuiltinArgs) {
		b.argMap = make(map[string]Arg)
		for _, a := range b.BuiltinArgs {
			b.argMap[a.Name] = a
		}
	}

	return b.argMap[name]
}

// GetBuiltins - Get an array of all the builtins
func GetBuiltins(commandContext *cmdctx.CmdContext) []Builtin {
	initBuiltins(commandContext)

	var builtarray []Builtin

	for _, v := range builtins {
		builtarray = append(builtarray, v)
	}

	return builtarray
}

// Internal function to load up builtins
func initBuiltins(commandContext *cmdctx.CmdContext) {
	if len(builtins) != 0 {
		return
	}
	builtins = make(map[string]Builtin)

	// Load all the internal defaults
	for _, rt := range basicbuiltins {
		builtins[rt.Name] = rt
	}

	builtinsfile, err := commandContext.GlobalConfig.GetString(flyctl.ConfigBuiltinsfile)
	if err != nil {
		fmt.Print(err)
		return
	}

	if builtinsfile == "" {
		return
	}

	filebuiltins, err := loadBuiltins(builtinsfile)
	if err != nil {
		fmt.Print(err)
		return
	}

	for _, rt := range filebuiltins {
		builtins[rt.Name] = rt
	}
}
