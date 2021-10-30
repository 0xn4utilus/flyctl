// Package cmd implements helpers useful to commands.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/superfly/flyctl/docstrings"
	"github.com/superfly/flyctl/internal/cli/internal/flag"
	"github.com/superfly/flyctl/internal/cli/internal/state"
	"github.com/superfly/flyctl/internal/client"
	"github.com/superfly/flyctl/internal/logger"
	"github.com/superfly/flyctl/internal/update"
	"github.com/superfly/flyctl/pkg/iostreams"
)

type (
	Preparer func(context.Context) (context.Context, error)

	Runner func(context.Context) error
)

func New(dsk string, fn Runner, p ...Preparer) *cobra.Command {
	ds := docstrings.Get(dsk)

	return Build(ds.Usage, ds.Short, ds.Long, fn, p...)
}

func Build(usage, short, long string, fn Runner, p ...Preparer) *cobra.Command {
	return &cobra.Command{
		Use:   usage,
		Short: short,
		Long:  long,
		RunE:  newRunE(fn, p...),
	}
}

var commonPreparers = []Preparer{
	determineWorkingDirectory,
	determineUserHomeDirectory,
	determineConfigDir,
	initClient,
	promptToUpdate,
}

func newRunE(fn Runner, preparers ...Preparer) func(*cobra.Command, []string) error {
	if fn == nil {
		return nil
	}

	return func(cmd *cobra.Command, _ []string) (err error) {
		ctx := cmd.Context()
		ctx = NewContext(ctx, cmd)
		ctx = flag.NewContext(ctx, cmd.Flags())

		// run the common preparers
		if ctx, err = prepare(ctx, commonPreparers...); err != nil {
			return
		}

		// run the preparers specific to the command
		if ctx, err = prepare(ctx, preparers...); err == nil {

			// and run the command
			err = fn(ctx)
		}

		return
	}
}

func prepare(parent context.Context, preparers ...Preparer) (ctx context.Context, err error) {
	ctx = parent

	for _, p := range preparers {
		if ctx, err = p(ctx); err != nil {
			break
		}
	}

	return
}

func determineWorkingDirectory(ctx context.Context) (context.Context, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("error determining working directory: %w", err)
	}

	logger.FromContext(ctx).
		Debugf("determined working directory: %q", wd)

	return state.WithWorkingDirectory(ctx, wd), nil
}

func determineUserHomeDirectory(ctx context.Context) (context.Context, error) {
	wd, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("error determining user home directory: %w", err)
	}

	logger.FromContext(ctx).
		Debugf("determined user home directory: %q", wd)

	return state.WithUserHomeDirectory(ctx, wd), nil
}

func determineConfigDir(ctx context.Context) (context.Context, error) {
	logger := logger.FromContext(ctx)

	dir := filepath.Join(state.UserHomeDirectory(ctx), ".fly")

	switch inf, err := os.Stat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		if err = os.MkdirAll(dir, 0700); err != nil {
			err = fmt.Errorf("error creating config directory: %w", err)

			return nil, err
		}

		logger.Debugf("created config directory at %s", dir)
	case err != nil:
		err = fmt.Errorf("error stat-ing config directory: %w", err)

		return nil, err
	case !inf.IsDir():
		err = fmt.Errorf("%s is not a directory", dir)

		return nil, err
	}

	logger.Debugf("determined config directory: %s", dir)

	return state.WithConfigDirectory(ctx, dir), nil
}

func promptToUpdate(ctx context.Context) (context.Context, error) {
	update.PromptFor(ctx, iostreams.FromContext(ctx))

	return ctx, nil
}

func initClient(ctx context.Context) (context.Context, error) {
	fs := flag.FromContext(ctx)
	fs.VisitAll(func(f *pflag.Flag) {
		fmt.Printf("%s: %v\n", f.Name, f.Value)
	})

	token := flag.GetAccessToken(ctx)

	return client.NewContext(ctx, client.FromToken(token)), nil
}

// RequireSession is a preparare which makes sure a session exists.
func RequireSession(ctx context.Context) (context.Context, error) {
	if !client.FromContext(ctx).Authenticated() {
		return nil, client.ErrNoAuthToken
	}

	return ctx, nil
}
