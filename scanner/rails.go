package scanner

import (
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

func configureRails(sourceDir string, config *ScannerConfig) (*SourceInfo, error) {
	if !checksPass(sourceDir, dirContains("Gemfile", "rails")) {
		return nil, nil
	}

	s := &SourceInfo{
		Family:   "Rails",
		Callback: RailsCallback,
	}

	// master.key comes with Rails apps from v5.2 onwards, but may not be present
	// if the app does not use Rails encrypted credentials.  Rails v6 added
	// support for multi-environment credentials.  Use the Rails searching
	// sequence for production credentials to determine the RAILS_MASTER_KEY.
	masterKey, err := os.ReadFile("config/credentials/production.key")
	if err != nil {
		masterKey, err = os.ReadFile("config/master.key")
	}

	if err == nil {
		s.Secrets = []Secret{
			{
				Key:   "RAILS_MASTER_KEY",
				Help:  "Secret key for accessing encrypted credentials",
				Value: string(masterKey),
			},
		}
	}

	s.SkipDeploy = true
	s.DeployDocs = `
Your Rails app is prepared for deployment.

Before proceeding, please review the posted Rails FAQ:
https://fly.io/docs/rails/getting-started/dockerfiles/.

Once ready: run 'fly deploy' to deploy your Rails app.
`

	return s, nil
}

func RailsCallback(srcInfo *SourceInfo, options map[string]bool) error {
	// install dockerfile-rails gem, if not already included
	gemfile, err := os.ReadFile("Gemfile")
	if err != nil {
		panic(err)
	} else if !strings.Contains(string(gemfile), "dockerfile-rails") {
		cmd := exec.Command("bundle", "add", "dockerfile-rails",
			"--optimistic", "--group", "development", "--skip-install")
		cmd.Stdin = nil
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "Failed to add dockerfile-rails gem, exiting")
		}

		cmd = exec.Command("bundle", "install", "--quiet")
		cmd.Stdin = nil
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "Failed to install dockerfile-rails gem, exiting")
		}
	}

	// generate Dockerfile if it doesn't already exist
	_, err = os.Stat("Dockerfile")
	if errors.Is(err, fs.ErrNotExist) {
		args := []string{"./bin/rails", "generate", "dockerfile",
			"--label=fly_launch_runtime:rails"}

		if options["postgresql"] {
			args = append(args, "--postgresql")
		}

		if options["redis"] {
			args = append(args, "--redis")
		}

		cmd := exec.Command("ruby", args...)
		cmd.Stdin = nil
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return errors.Wrap(err, "Failed to generate Dockefile")
		}
	}

	// read dockerfile
	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		return errors.Wrap(err, "Dockerfile not found")
	}

	// extract port
	port := 3000
	re := regexp.MustCompile(`(?m)^EXPOSE\s+(?P<port>\d+)`)
	m := re.FindStringSubmatch(string(dockerfile))

	for i, name := range re.SubexpNames() {
		if len(m) > 0 && name == "port" {
			port, err = strconv.Atoi(m[i])
			if err != nil {
				panic(err)
			}
		}
	}
	srcInfo.Port = port

	// extract workdir
	workdir := "/rails"
	re = regexp.MustCompile(`(?m).*^WORKDIR\s+(?P<dir>/\S+)`)
	m = re.FindStringSubmatch(string(dockerfile))

	for i, name := range re.SubexpNames() {
		if len(m) > 0 && name == "dir" {
			workdir = m[i]
		}
	}

	srcInfo.Statics = []Static{
		{
			GuestPath: workdir + "/public",
			UrlPrefix: "/",
		},
	}

	return nil
}
