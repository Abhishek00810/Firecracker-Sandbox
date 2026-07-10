package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var ErrSessionNotFound = errors.New("session not found")

// SessionRecord is the authenticated user behind a live Better Auth session.
type SessionRecord struct {
	UserID string `json:"user_id"`
}

// ResolveSession validates an opaque Better Auth token against the session
// table. The expiry comparison happens in Postgres, so expired rows never
// authenticate even if the application server clock format changes.
func (c *Client) ResolveSession(token string) (SessionRecord, error) {
	base, err := url.Parse(c.baseURL + "/rest/v1/session")
	if err != nil {
		return SessionRecord{}, fmt.Errorf("parse session url: %w", err)
	}

	q := url.Values{}
	q.Set("token", "eq."+token)
	q.Set("expires_at", "gt."+time.Now().UTC().Format(time.RFC3339Nano))
	q.Set("select", "user_id")
	q.Set("limit", "1")
	base.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, base.String(), nil)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("build session request: %w", err)
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("session request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SessionRecord{}, fmt.Errorf("session lookup returned status %d", resp.StatusCode)
	}

	var rows []SessionRecord
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return SessionRecord{}, fmt.Errorf("decode session response: %w", err)
	}
	if len(rows) == 0 || rows[0].UserID == "" {
		return SessionRecord{}, ErrSessionNotFound
	}
	return rows[0], nil
}
