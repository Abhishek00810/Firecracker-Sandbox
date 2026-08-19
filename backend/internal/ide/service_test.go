package ide

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"backend/internal/executor"
)

type fakeRunner struct {
	commands []string
	result   executor.ExecutionResult
	err      error
}

func (f *fakeRunner) Exec(_ context.Context, _ string, command string, _ int) (executor.ExecutionResult, error) {
	f.commands = append(f.commands, command)
	return f.result, f.err
}

func TestServiceStartsOpenVSCodeWithoutApplicationAuthentication(t *testing.T) {
	runner := &fakeRunner{}
	service, err := NewService(runner, Config{})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := service.Start(context.Background(), "sandbox-1")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Port != DefaultPort || instance.State != "ready" {
		t.Fatalf("instance=%+v", instance)
	}
	command := runner.commands[0]
	if output, err := exec.Command("sh", "-n", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("invalid start command: %v: %s", err, output)
	}
	for _, expected := range []string{
		"/opt/openvscode-server/bin/openvscode-server",
		"--host 0.0.0.0",
		"--port 3001",
		"--without-connection-token",
		"'/workspace'",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("start command missing %q: %s", expected, command)
		}
	}
}

func TestServiceRejectsRelativePaths(t *testing.T) {
	if _, err := NewService(&fakeRunner{}, Config{Binary: "openvscode-server"}); err == nil {
		t.Fatal("expected relative binary to be rejected")
	}
}

func TestServiceReportsCommandFailure(t *testing.T) {
	runner := &fakeRunner{result: executor.ExecutionResult{ExitCode: 127, Stderr: "binary missing"}}
	service, _ := NewService(runner, Config{})
	if _, err := service.Start(context.Background(), "sandbox-1"); err == nil || !strings.Contains(err.Error(), "binary missing") {
		t.Fatalf("start error=%v", err)
	}
	runner.err = errors.New("sandbox unavailable")
	if _, err := service.Status(context.Background(), "sandbox-1"); err == nil || !strings.Contains(err.Error(), "sandbox unavailable") {
		t.Fatalf("status error=%v", err)
	}
}
