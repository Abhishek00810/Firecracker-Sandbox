package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

// UsageLog represents a single execution event to be recorded in the usage_logs table.
type UsageLog struct {
	APIKeyID      string  `json:"api_key_id"`
	UserID        string  `json:"user_id"`
	ExecutionType string  `json:"execution_type"`
	Language      string  `json:"language"`
	DurationMs    int     `json:"duration_ms"`
	ExitCode      int     `json:"exit_code"`
	CostUSD       float64 `json:"cost_usd"`
}

// InsertUsageLog POSTs a usage event to Supabase. Intended to be called in a goroutine.
// Failures are logged with slog.Warn and do not propagate to the caller.
func (c *Client) InsertUsageLog(ctx context.Context, log UsageLog) {
	body, err := json.Marshal(log)
	if err != nil {
		slog.Warn("usage log marshal failed: ", "err", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/v1/usage_logs", bytes.NewReader(body))
	if err != nil {
		slog.Warn("usage log request build failed: ", "err", err)
		return
	}

	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")

	resp, err := c.http.Do(req)
	if err != nil {
		slog.Warn("usage log insert failed", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Warn("usage log insert returned unexpected status", "status", resp.StatusCode,
			"err", fmt.Errorf("supabase returned status: %d", resp.StatusCode))
	}
}
