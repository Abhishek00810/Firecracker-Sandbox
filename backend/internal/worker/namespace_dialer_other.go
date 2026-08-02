//go:build !linux

package worker

import (
	"context"
	"errors"
	"net"
)

func (systemNamespaceDialer) DialContext(context.Context, int, string) (net.Conn, error) {
	return nil, errors.New("sandbox network namespaces require Linux")
}
