package platform

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrSessionNotFound = errors.New("session not found")

type SessionRecord struct {
	UserID string `json:"user_id"`
}

func (c *Client) ResolveSession(token string) (SessionRecord, error) {
	var record SessionRecord
	err := c.pool.QueryRow(context.Background(), `
		SELECT user_id::text FROM session
		WHERE token = $1 AND expires_at > now()
		LIMIT 1`, token).Scan(&record.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionRecord{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionRecord{}, fmt.Errorf("resolve session: %w", err)
	}
	return record, nil
}
