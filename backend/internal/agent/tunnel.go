package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"time"
)

// StartTunnel opens (and keeps alive) an SSH local-forward from
// 127.0.0.1:localPort to the agent's 127.0.0.1:remotePort on the worker host.
//
// sshCommand is the user's alias-based command, e.g. "ssh ro"; host/user/key
// live in ~/.ssh/config. The forward is (re)spawned in a background goroutine
// that restarts it if the ssh process exits, until ctx is cancelled. It returns
// once the local port accepts connections.
func StartTunnel(ctx context.Context, sshCommand, localPort, remotePort string) error {
	fields := strings.Fields(sshCommand)
	if len(fields) == 0 {
		return fmt.Errorf("empty SSH_COMMAND")
	}
	// Reuse a tunnel from a previous run if the local port already forwards —
	// makes control-plane restarts idempotent instead of colliding on the port.
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+localPort, time.Second); err == nil {
		conn.Close()
		slog.Info("reusing existing agent tunnel", "port", localPort)
		return nil
	}

	forward := fmt.Sprintf("%s:127.0.0.1:%s", localPort, remotePort)
	args := append(fields[1:],
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-L", forward,
	)

	go func() {
		for ctx.Err() == nil {
			cmd := exec.CommandContext(ctx, fields[0], args...)
			slog.Info("opening agent SSH tunnel", "forward", forward, "via", sshCommand)
			if err := cmd.Run(); err != nil && ctx.Err() == nil {
				slog.Warn("agent SSH tunnel exited; retrying", "err", err)
				time.Sleep(2 * time.Second)
			}
		}
	}()

	// Wait for the forward to come up.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+localPort, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("agent tunnel to %s did not come up within 15s", forward)
}
