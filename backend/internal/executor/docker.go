package executor

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

type DockerExecutor struct {
	Client *client.Client
}

func (e *DockerExecutor) Execute(code string, language string) (string, error) {
	ctx := context.Background()

	// 1. container create
	resp, err := e.Client.ContainerCreate(ctx, &container.Config{
		Image: "python:alpine",
		Cmd:   []string{"python", "-c", code},
	}, nil, nil, nil, "")

	if err != nil {
		return "", err
	}

	// 2. container start

	err = e.Client.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		return "", err
	}

	// 3. container waiting

	statusCh, errCh := e.Client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)

	select {
	case err := <-errCh:
		if err != nil {
			return "", err
		}
	case <-statusCh:
	}

	// 4. get output (logs)

	output, err := e.Client.ContainerLogs(ctx, resp.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})

	if err != nil {
		return "", err
	}

	// read output

}

func NewDockerExecutor(ctx context.Context) (*DockerExecutor, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}

	// Explicit health check
	if _, err := cli.Ping(ctx); err != nil {
		return nil, err
	}

	return &DockerExecutor{Client: cli}, nil
}
