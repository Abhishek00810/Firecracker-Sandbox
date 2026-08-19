package ide

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"backend/internal/executor"
)

const DefaultPort uint16 = 3001

type CommandRunner interface {
	Exec(context.Context, string, string, int) (executor.ExecutionResult, error)
}

type Manager interface {
	Start(context.Context, string) (Instance, error)
	Status(context.Context, string) (Instance, error)
	Stop(context.Context, string) error
}

type Config struct {
	Binary    string
	Workspace string
	Port      uint16
}

type Instance struct {
	Port  uint16 `json:"port"`
	State string `json:"state"`
}

type Service struct {
	runner CommandRunner
	config Config
}

func NewService(runner CommandRunner, config Config) (*Service, error) {
	if runner == nil {
		return nil, errors.New("IDE command runner is required")
	}
	if config.Binary == "" {
		config.Binary = "/opt/openvscode-server/bin/openvscode-server"
	}
	if config.Workspace == "" {
		config.Workspace = "/workspace"
	}
	if config.Port == 0 {
		config.Port = DefaultPort
	}
	if !filepath.IsAbs(config.Binary) || !filepath.IsAbs(config.Workspace) {
		return nil, errors.New("IDE binary and workspace must be absolute paths")
	}
	return &Service{runner: runner, config: config}, nil
}

func (s *Service) Start(ctx context.Context, sandboxID string) (Instance, error) {
	result, err := s.runner.Exec(ctx, sandboxID, s.startCommand(), 45)
	if err != nil {
		return Instance{}, fmt.Errorf("start IDE: %w", err)
	}
	if result.ExitCode != 0 {
		return Instance{}, fmt.Errorf("start IDE: %s", commandError(result))
	}
	return Instance{Port: s.config.Port, State: "ready"}, nil
}

func (s *Service) Status(ctx context.Context, sandboxID string) (Instance, error) {
	result, err := s.runner.Exec(ctx, sandboxID, s.statusCommand(), 5)
	if err != nil {
		return Instance{}, fmt.Errorf("check IDE status: %w", err)
	}
	state := strings.TrimSpace(result.Stdout)
	if state != "ready" {
		state = "stopped"
	}
	return Instance{Port: s.config.Port, State: state}, nil
}

func (s *Service) Stop(ctx context.Context, sandboxID string) error {
	result, err := s.runner.Exec(ctx, sandboxID, s.stopCommand(), 10)
	if err != nil {
		return fmt.Errorf("stop IDE: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("stop IDE: %s", commandError(result))
	}
	return nil
}

func (s *Service) startCommand() string {
	port := strconv.Itoa(int(s.config.Port))
	command := strings.Join([]string{
		"set -eu",
		"state_dir=/var/lib/renderops/ide",
		"pid_file=$state_dir/openvscode.pid",
		"log_file=$state_dir/openvscode.log",
		"mkdir -p \"$state_dir\" " + shellQuote(s.config.Workspace),
		"if [ -s \"$pid_file\" ] && kill -0 \"$(cat \"$pid_file\")\" 2>/dev/null; then exit 0; fi",
		"rm -f \"$pid_file\"",
		"[ -x " + shellQuote(s.config.Binary) + " ] || { printf 'OpenVSCode is not installed at %s\\n' " + shellQuote(s.config.Binary) + " >&2; exit 127; }",
		"nohup " + shellQuote(s.config.Binary) +
			" --host 0.0.0.0 --port " + port +
			" --without-connection-token " + shellQuote(s.config.Workspace) +
			" >\"$log_file\" 2>&1 </dev/null & pid=$!",
		"printf '%s\\n' \"$pid\" >\"$pid_file\"",
		"i=0; while [ \"$i\" -lt 30 ]; do wget -q -O /dev/null http://127.0.0.1:" + port + "/ && exit 0; kill -0 \"$pid\" 2>/dev/null || break; i=$((i + 1)); sleep 1; done",
		"tail -n 20 \"$log_file\" >&2 || true",
		"exit 1",
	}, "; ")
	return "( " + command + " )"
}

func (s *Service) statusCommand() string {
	return `pid_file=/var/lib/renderops/ide/openvscode.pid; if [ -s "$pid_file" ] && kill -0 "$(cat "$pid_file")" 2>/dev/null; then printf ready; else printf stopped; fi`
}

func (s *Service) stopCommand() string {
	return `pid_file=/var/lib/renderops/ide/openvscode.pid; if [ -s "$pid_file" ]; then pid=$(cat "$pid_file"); kill "$pid" 2>/dev/null || true; i=0; while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 10 ]; do i=$((i + 1)); sleep 1; done; kill -9 "$pid" 2>/dev/null || true; fi; rm -f "$pid_file"`
}

func commandError(result executor.ExecutionResult) string {
	if message := strings.TrimSpace(result.Stderr); message != "" {
		return message
	}
	return fmt.Sprintf("command exited with status %d", result.ExitCode)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
