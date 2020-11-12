package docker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/dustin/go-humanize"
	"github.com/mattn/go-isatty"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/cmdctx"
	"github.com/superfly/flyctl/flyctl"
	"github.com/superfly/flyctl/terminal"
	"golang.org/x/net/context"
)

type DeployOperation struct {
	ctx             context.Context
	dockerClient    *DockerClient
	apiClient       *api.Client
	dockerAvailable bool
	out             io.Writer
	appName         string
	appConfig       *flyctl.AppConfig
	imageTag        string
	remoteOnly      bool
	localOnly       bool
}

func NewDeployOperation(ctx context.Context, cmdContext *cmdctx.CmdContext) (*DeployOperation, error) {
	dockerClient, err := NewDockerClient()
	if err != nil {
		return nil, err
	}

	//squash:=cmdContext.Config.GetBool("squash")
	remoteOnly := cmdContext.Config.GetBool("remote-only")
	localOnly := cmdContext.Config.GetBool("local-only")

	imageLabel, _ := cmdContext.Config.GetString("image-label")

	op := &DeployOperation{
		ctx:          ctx,
		dockerClient: dockerClient,
		apiClient:    cmdContext.Client.API(),
		out:          cmdContext.Out,
		appName:      cmdContext.AppName,
		appConfig:    cmdContext.AppConfig,
		imageTag:     newDeploymentTag(cmdContext.AppName, imageLabel),
		localOnly:    localOnly,
		remoteOnly:   remoteOnly,
	}

	op.dockerAvailable = op.dockerClient.Check(ctx) == nil

	if localOnly && remoteOnly {
		return nil, fmt.Errorf("Both --local-only and --remote-only are set - select only one")
	}

	return op, nil
}

func (op *DeployOperation) AppName() string {
	if op.appName != "" {
		return op.appName
	}
	return op.appConfig.AppName
}

func (op *DeployOperation) DockerAvailable() bool {
	return op.dockerAvailable
}

func (op *DeployOperation) LocalOnly() bool {
	return op.localOnly
}

func (op *DeployOperation) RemoteOnly() bool {
	return op.remoteOnly
}

type DeploymentStrategy string

const (
	CanaryDeploymentStrategy    DeploymentStrategy = "canary"
	RollingDeploymentStrategy   DeploymentStrategy = "rolling"
	ImmediateDeploymentStrategy DeploymentStrategy = "immediate"
	DefaultDeploymentStrategy   DeploymentStrategy = ""
)

func ParseDeploymentStrategy(val string) (DeploymentStrategy, error) {
	switch val {
	case "canary":
		return CanaryDeploymentStrategy, nil
	case "rolling":
		return RollingDeploymentStrategy, nil
	case "immediate":
		return ImmediateDeploymentStrategy, nil
	default:
		return "", fmt.Errorf("Unknown deployment strategy '%s'", val)
	}
}

func (op *DeployOperation) ValidateConfig() (*api.AppConfig, error) {
	if op.appConfig == nil {
		op.appConfig = flyctl.NewAppConfig()
	}

	parsedConfig, err := op.apiClient.ParseConfig(op.appName, op.appConfig.Definition)
	if err != nil {
		return parsedConfig, err
	}

	if !parsedConfig.Valid {
		return parsedConfig, errors.New("App configuration is not valid")
	}

	op.appConfig.Definition = parsedConfig.Definition

	return parsedConfig, nil
}

func (op *DeployOperation) ResolveImageLocally(ctx context.Context, commandContext *cmdctx.CmdContext, imageRef string) (*Image, error) {
	commandContext.Status("deploy", "Resolving image")

	if !op.DockerAvailable() || op.RemoteOnly() {
		return nil, nil
	}

	imgSummary, err := op.dockerClient.findImage(ctx, imageRef)
	if err != nil {
		return nil, err
	}

	if imgSummary == nil {
		return nil, nil
	}

	commandContext.Statusf("deploy", cmdctx.SINFO, "Image ID: %+v\n", imgSummary.ID)
	commandContext.Statusf("deploy", cmdctx.SINFO, "Image size: %s\n", humanize.Bytes(uint64(imgSummary.Size)))

	commandContext.Status("deploy", cmdctx.SDONE, "Image resolving done")

	commandContext.Status("deploy", cmdctx.SBEGIN, "Creating deployment tag")
	if err := op.dockerClient.TagImage(op.ctx, imgSummary.ID, op.imageTag); err != nil {
		return nil, err
	}
	commandContext.Status("deploy", cmdctx.SINFO, "-->", op.imageTag)

	image := &Image{
		ID:   imgSummary.ID,
		Size: imgSummary.Size,
		Tag:  op.imageTag,
	}

	err = op.PushImage(*image)

	if err != nil {
		return nil, err
	}

	return image, nil
}

func (op *DeployOperation) resolveImageWithoutDocker(ctx context.Context, imageRef string) (*Image, error) {
	ref, err := CheckManifest(op.ctx, imageRef, "")
	if err != nil {
		return nil, err
	}

	image := Image{
		Tag: ref.Repository(),
	}

	return &image, nil
}

func (op *DeployOperation) pushImage(imageTag string) error {

	if imageTag == "" {
		return errors.New("invalid image reference")
	}

	if err := op.dockerClient.PushImage(op.ctx, imageTag, op.out); err != nil {
		return err
	}

	return nil
}

func (op *DeployOperation) optimizeImage(imageTag string) error {
	if isatty.IsTerminal(os.Stdout.Fd()) {
		s := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
		s.Writer = os.Stderr
		s.Prefix = "building fs... "
		s.Start()
		defer s.Stop()
	}

	delay := 0 * time.Second

	for {
		select {
		case <-time.After(delay):
			status, err := op.apiClient.OptimizeImage(op.AppName(), imageTag)
			if err != nil {
				return err
			}
			if status != "in_progress" {
				return nil
			}
			delay = 1 * time.Second
		case <-op.ctx.Done():
			return op.ctx.Err()
		}
	}
}

func (op *DeployOperation) Deploy(imageRef string, strategy DeploymentStrategy) (*api.Release, error) {
	return op.deployImage(imageRef, strategy)
}

func (op *DeployOperation) deployImage(imageTag string, strategy DeploymentStrategy) (*api.Release, error) {
	input := api.DeployImageInput{AppID: op.AppName(), Image: imageTag}
	if strategy != DefaultDeploymentStrategy {
		input.Strategy = api.StringPointer(strings.ToUpper(string(strategy)))
	}

	if op.appConfig != nil && len(op.appConfig.Definition) > 0 {
		x := api.Definition(op.appConfig.Definition)
		input.Definition = &x
	}

	release, err := op.apiClient.DeployImage(input)
	if err != nil {
		return nil, err
	}
	return release, err
}

func (op *DeployOperation) CleanDeploymentTags() {
	if !op.dockerAvailable {
		return
	}
	err := op.dockerClient.DeleteDeploymentImages(op.ctx, op.imageTag)
	if err != nil {
		terminal.Debugf("Error cleaning deployment tags: %s", err)
	}
}
