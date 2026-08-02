package worker

import (
	"context"
	"net"
)

type namespaceDialer interface {
	DialContext(context.Context, int, string) (net.Conn, error)
}

type systemNamespaceDialer struct{}
