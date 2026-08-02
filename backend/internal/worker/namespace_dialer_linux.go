//go:build linux

package worker

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func (systemNamespaceDialer) DialContext(ctx context.Context, slot int, address string) (net.Conn, error) {
	type result struct {
		connection net.Conn
		err        error
	}
	results := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		unlockThread := true
		defer func() {
			if unlockThread {
				runtime.UnlockOSThread()
			}
		}()

		current, err := os.Open("/proc/self/ns/net")
		if err != nil {
			results <- result{err: fmt.Errorf("open current network namespace: %w", err)}
			return
		}
		defer current.Close()
		target, err := os.Open(fmt.Sprintf("/var/run/netns/fc-ns-%d", slot))
		if err != nil {
			results <- result{err: fmt.Errorf("open sandbox network namespace: %w", err)}
			return
		}
		defer target.Close()
		if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
			results <- result{err: fmt.Errorf("enter sandbox network namespace: %w", err)}
			return
		}
		connection, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		restoreErr := unix.Setns(int(current.Fd()), unix.CLONE_NEWNET)
		if restoreErr != nil {
			// Exiting a goroutine while it remains locked makes the runtime discard
			// this contaminated OS thread instead of scheduling other Go work on it.
			unlockThread = false
			if connection != nil {
				_ = connection.Close()
			}
			results <- result{err: fmt.Errorf("restore worker network namespace: %w", restoreErr)}
			return
		}
		results <- result{connection: connection, err: dialErr}
	}()

	select {
	case result := <-results:
		return result.connection, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
