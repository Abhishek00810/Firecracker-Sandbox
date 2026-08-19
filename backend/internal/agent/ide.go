package agent

import (
	"context"

	"backend/internal/plane"
)

type IDEClient struct {
	workerToken string
}

func NewIDEClient(workerToken string) *IDEClient {
	return &IDEClient{workerToken: workerToken}
}

func (c *IDEClient) StartIDE(ctx context.Context, endpoint, sandboxID string) (plane.IDEInstance, error) {
	return NewClient(endpoint, c.workerToken).StartIDE(ctx, sandboxID)
}
