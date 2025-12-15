package executor

import (
	"bytes"
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type DockerExecutor struct {
	Client *client.Client
}

type ExecutionResult struct {
	Output   string  `json:"output"`
	Duration float64 `json:"duration"` // Changed to float64 for seconds
}

func (e *DockerExecutor) Execute(code string, language string) (ExecutionResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. container create
	resp, err := e.Client.ContainerCreate(ctx, &container.Config{
		Image: "python:alpine",
		Cmd:   []string{"python", "-c", code},

		//disable network for container
		NetworkDisabled: true,
	}, &container.HostConfig{
		Resources: container.Resources{
			Memory:   128 * 1024 * 1024,
			NanoCPUs: 500000000,
		},
	}, nil, nil, "")

	if err != nil {
		return ExecutionResult{}, err
	}

	// close and kill the container
	defer func() {
		_ = e.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{
			Force: true,
		})
	}()

	// 2. container start

	err = e.Client.ContainerStart(ctx, resp.ID, container.StartOptions{})
	if err != nil {
		return ExecutionResult{}, err
	}

	startTime := time.Now()

	// 3. container waiting

	statusCh, errCh := e.Client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)

	select {
	case err := <-errCh:
		if err != nil {
			return ExecutionResult{}, err
		}
	case <-statusCh:
	case <-ctx.Done():
		return ExecutionResult{}, ctx.Err()
	}
	//end timing here
	executionTime := time.Since(startTime)

	// 4. get output (logs)

	output, err := e.Client.ContainerLogs(ctx, resp.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})

	if err != nil {
		return ExecutionResult{}, err
	}

	// read output

	defer output.Close()
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, output)
	if err != nil {
		return ExecutionResult{}, err
	}
	finalOutput := stdout.String() + stderr.String()

	return ExecutionResult{
		Output:   finalOutput,
		Duration: executionTime.Seconds(),
	}, nil
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
