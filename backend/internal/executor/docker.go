package executor

import (
	"context"

	"github.com/docker/docker/client"
)

type DockerExecutor struct {
	Client *client.Client
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
