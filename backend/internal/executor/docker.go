package executor

import (
	"bytes"
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-units"
)

type DockerExecutor struct {
	Client *client.Client
}

type ExecutionResult struct {
	Output            string  `json:"output"`
	Duration          float64 `json:"duration"` // Changed to float64 for seconds
	ExitCode          int64   `json:"exit_code"`
	TerminationReason string  `json:"termination_reason,omitempty"`
}

func (e *DockerExecutor) Execute(ctx context.Context, code string, language string) (ExecutionResult, error) {

	// 1. container create
	resp, err := e.Client.ContainerCreate(ctx, &container.Config{
		Image: "python:alpine",
		Cmd:   []string{"python", "-c", code},

		//disable network for container
		NetworkDisabled: true,
		User:            "1000",
	}, &container.HostConfig{
		NetworkMode: "none", //silent prison network lockdown

		// filesystem lockdown
		ReadonlyRootfs: true, // read only filesystem but we need something for write for a user
		// lets create a temp for that
		Tmpfs: map[string]string{
			"/tmp": "", // empty string = default tmpfs options where user will write
		},

		//privilege lockdown just like not giving chance for attacks such as dirty cow
		Privileged:  false,           //this is default, but we wont give any chance god mode disabled
		CapDrop:     []string{"ALL"}, // drop all root capabilities
		SecurityOpt: []string{"no-new-privileges"},
		//prevent to create more processed

		Resources: container.Resources{
			Memory:   128 * 1024 * 1024,
			NanoCPUs: 500000000,
			PidsLimit: func() *int64 {
				i := int64(20) // Create the variable inside
				return &i      // Return the pointer
			}(),
			Ulimits: []*units.Ulimit{
				{Name: "nofile", Soft: 1024, Hard: 1024},        // Max Open files
				{Name: "nproc", Soft: 50, Hard: 50},             // max processes (redundant wiht pidslimit)
				{Name: "fsize", Soft: 10485760, Hard: 10485760}, // Max file size: 10MB}
				{Name: "core", Soft: 0, Hard: 0},                // no core dumps
			},
		},
	}, nil, nil, "")

	if err != nil {
		return ExecutionResult{}, err
	}

	// close and kill the container
	defer func() {
		cleanupCtx := context.Background()
		_ = e.Client.ContainerStop(cleanupCtx, resp.ID, container.StopOptions{})
		_ = e.Client.ContainerRemove(cleanupCtx, resp.ID, container.RemoveOptions{
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
	var ExitCode int64 = -1
	var TerminationReason string = ""

	select {
	case err := <-errCh:
		if err != nil {
			return ExecutionResult{}, err
		}
	case status := <-statusCh:
		ExitCode = status.StatusCode
		switch ExitCode {
		case 137:
			TerminationReason = "oom_kill" // Docker killed it
		case 143:
			TerminationReason = "graceful_stop" // Docker stopped gracefully
		case 0:
			TerminationReason = "success"
		default:
			TerminationReason = "runtime_error"
		}
	case <-ctx.Done():
		TerminationReason = "timeout"
		ExitCode = -1
		return ExecutionResult{
			ExitCode:          ExitCode,
			TerminationReason: TerminationReason,
		}, ctx.Err()
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
		Output:            finalOutput,
		Duration:          executionTime.Seconds(),
		ExitCode:          ExitCode,
		TerminationReason: TerminationReason,
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
