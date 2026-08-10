package platform

import (
	"context"
	"encoding/json"
	"fmt"
)

type AuditEvent struct {
	ScopeType     string
	ScopeID       string
	ActorType     string
	ActorUserID   string
	ActorAPIKeyID string
	Action        string
	ResourceType  string
	ResourceID    string
	Outcome       string
	RequestID     string
	IPAddress     string
	UserAgent     string
	Metadata      map[string]any
}

func (c *Client) InsertAuditEvent(ctx context.Context, event AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	if event.Metadata == nil {
		metadata = []byte("{}")
	}

	var actorUserID, actorAPIKeyID any
	if event.ActorUserID != "" {
		actorUserID = event.ActorUserID
	}
	if event.ActorAPIKeyID != "" {
		actorAPIKeyID = event.ActorAPIKeyID
	}

	_, err = c.pool.Exec(ctx, `
		INSERT INTO audit_events (
			scope_type, scope_id, actor_type, actor_user_id, actor_api_key_id,
			action, resource_type, resource_id, outcome, request_id,
			ip_address, user_agent, metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		event.ScopeType,
		event.ScopeID,
		event.ActorType,
		actorUserID,
		actorAPIKeyID,
		event.Action,
		event.ResourceType,
		event.ResourceID,
		event.Outcome,
		event.RequestID,
		event.IPAddress,
		event.UserAgent,
		metadata,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}
