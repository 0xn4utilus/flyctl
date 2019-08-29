package docker

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/docker/docker/builder/dockerignore"
	"github.com/superfly/flyctl/api"
	"github.com/superfly/flyctl/flyctl"
	"github.com/superfly/flyctl/helpers"
	"github.com/superfly/flyctl/terminal"
)

type DockerfileSource uint

const (
	BuilderDockerfile DockerfileSource = iota
	ProjectDockerfile
	NoDockerfile
)

var ErrNoDockerfile = errors.New("Project does not contain a Dockerfile or specify a builder")
var ErrDockerDaemon = errors.New("Docker daemon must be running to perform this action")

func dockerfileSource(project *flyctl.Project) DockerfileSource {
	if _, err := os.Stat(path.Join(project.ProjectDir, "Dockerfile")); err == nil {
		return ProjectDockerfile
	}
	if project.Builder() != "" {
		return BuilderDockerfile
	}
	return NoDockerfile
}

func (op *DeployOperation) BuildAndDeploy(project *flyctl.Project) (*api.Release, error) {
	if !op.DockerAvailable() {
		return nil, ErrDockerDaemon
	}

	buildContext, err := NewBuildContext()
	if err != nil {
		return nil, err
	}
	defer buildContext.Close()

	s := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
	s.Writer = os.Stderr
	s.Prefix = "Creating build context... "
	s.Start()

	excludes, err := readDockerignore(project.ProjectDir)
	if err != nil {
		return nil, err
	}

	if err := buildContext.AddSource(project.ProjectDir, excludes); err != nil {
		return nil, err
	}

	s.Stop()

	switch dockerfileSource(project) {
	case NoDockerfile:
		return nil, ErrNoDockerfile
	case ProjectDockerfile:
		fmt.Println("Using Dockerfile from project:", path.Join(project.ProjectDir, "Dockerfile"))
	case BuilderDockerfile:
		fmt.Println("Using builder:", project.Builder())
		builderPath, err := fetchBuilder(project.Builder(), project.ProjectDir)
		defer os.RemoveAll(builderPath)
		if err != nil {
			return nil, err
		}
		if err := buildContext.AddSource(builderPath, []string{}); err != nil {
			return nil, err
		}
	}

	archive, err := buildContext.Archive()
	if err != nil {
		return nil, err
	}
	defer archive.Close()

	tag := newDeploymentTag(op.AppName)

	buildArgs := normalizeBuildArgs(project.BuildArgs())

	if err := op.dockerClient.BuildImage(archive.File, tag, buildArgs, op.out); err != nil {
		return nil, err
	}

	if err := op.pushImage(tag); err != nil {
		return nil, err
	}

	release, err := op.deployImage(tag)
	if err != nil {
		return nil, err
	}

	op.cleanDeploymentTags()

	return release, nil
}

func (op *DeployOperation) StartRemoteBuild(project *flyctl.Project) (*api.Build, error) {
	if dockerfileSource(project) == NoDockerfile {
		return nil, ErrNoDockerfile
	}

	buildContext, err := NewBuildContext()
	if err != nil {
		return nil, err
	}
	defer buildContext.Close()

	s := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
	s.Writer = os.Stderr
	s.Prefix = "Creating build context... "
	s.Start()

	excludes, err := readGitignore(project.ProjectDir)
	if err != nil {
		return nil, err
	}

	if err := buildContext.AddSource(project.ProjectDir, excludes); err != nil {
		return nil, err
	}

	ctxFile, err := buildContext.Archive()
	if err != nil {
		return nil, err
	}
	s.Stop()

	s.Prefix = "Submitting build..."

	uploadFileName := fmt.Sprintf("source-%d.tar.gz", time.Now().Unix())
	getURL, putURL, err := op.apiClient.CreateSignedUrls(op.AppName, uploadFileName)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("PUT", putURL, ctxFile)
	if err != nil {
		return nil, err
	}
	req.ContentLength = ctxFile.Size

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("Error submitting build: %s", body)
	}

	build, err := op.apiClient.CreateBuild(op.AppName, getURL, "targz")
	if err != nil {
		return nil, err
	}
	s.Stop()

	return build, nil
}

func normalizeBuildArgs(args map[string]string) map[string]*string {
	var out = map[string]*string{}

	for k, v := range args {
		k = strings.ToUpper(k)
		// docker needs a string pointer. since ranges reuse variables we need to deref a copy
		val := v
		out[k] = &val
	}

	return out
}

func isDockerfilePath(imageName string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	maybePath := path.Join(cwd, imageName)

	return helpers.FileExists(maybePath)
}

func isDirContainingDockerfile(imageName string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	maybePath := path.Join(cwd, imageName, "Dockerfile")

	return helpers.FileExists(maybePath)
}

func resolveBuildPath(imageRef string) (string, error) {
	if isDockerfilePath(imageRef) {
		fmt.Printf("found file at '%s'\n", imageRef)
		return path.Dir(imageRef), nil
	} else if isDirContainingDockerfile(imageRef) {
		fmt.Printf("found Dockerfile in '%s'\n", imageRef)
		return imageRef, nil
	} else if strings.HasPrefix(imageRef, ".") {
		fmt.Printf("'%s' is a local path\n", imageRef)
		return filepath.Abs(imageRef)
	}

	return "", errors.New("Invalid build path")
}

func recursivelyFindFilesInParents(startingDir, name string) ([]string, error) {
	matches := []string{}
	dir, err := filepath.Abs(filepath.Clean(startingDir))
	if err != nil {
		return matches, err
	}

	for {
		filename := filepath.Join(dir, name)
		if _, err := os.Stat(filename); err == nil {
			matches = append(matches, filename)
		}
		dir = filepath.Dir(dir)
		if dir == "/" {
			break
		}
	}

	return matches, nil
}

func readDockerignore(workingDir string) ([]string, error) {
	file, err := os.Open(path.Join(workingDir, ".dockerignore"))
	if os.IsNotExist(err) {
		return []string{}, nil
	} else if err != nil {
		terminal.Warn("Error reading dockerignore", err)
		return []string{}, nil
	}

	return dockerignore.ReadAll(file)
}

func readGitignore(workingDir string) ([]string, error) {
	file, err := os.Open(path.Join(workingDir, ".gitignore"))
	if os.IsNotExist(err) {
		return []string{}, nil
	} else if err != nil {
		terminal.Warn("Error reading gitignore", err)
		return []string{}, nil
	}

	return dockerignore.ReadAll(file)
}
