package metering

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) Submit(ctx context.Context, samples []Sample) error {
	body, err := json.Marshal(Batch{Samples: samples})
	if err != nil {
		return fmt.Errorf("encode usage batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+Route, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("submit usage batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	message, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	return fmt.Errorf("submit usage batch: status %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
}
