package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

type Sandbox struct {
	ID           string         `json:"id"`
	UserID       string         `json:"user_id"`
	APIKeyID     string         `json:"api_key_id,omitempty"`
	Name         string         `json:"name"`
	State        string         `json:"state"`
	BillingModel string         `json:"billing_model"`
	VCPUs        int            `json:"vcpus"`
	MemoryMB     int            `json:"memory_mb"`
	DiskGB       int            `json:"disk_gb"`
	Internet     bool           `json:"internet"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type SandboxListItem struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	State        string         `json:"state"`
	BillingModel string         `json:"billing_model"`
	VCPUs        int            `json:"vcpus"`
	MemoryMB     int            `json:"memory_mb"`
	DiskGB       int            `json:"disk_gb"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	Created      string         `json:"created_at,omitempty"`
}

func (c *Client) ListSandboxes(ctx context.Context, userID string) ([]SandboxListItem, error) {
	rows, err := c.pool.Query(ctx, `SELECT id::text,name,state,billing_model,vcpus,memory_mb,disk_gb,COALESCE(metadata,'{}'::jsonb),created_at FROM sandboxes WHERE user_id=$1 AND state<>'destroyed' ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	defer rows.Close()
	items := make([]SandboxListItem, 0)
	for rows.Next() {
		var item SandboxListItem
		var metadata []byte
		var created time.Time
		if err := rows.Scan(&item.ID, &item.Name, &item.State, &item.BillingModel, &item.VCPUs, &item.MemoryMB, &item.DiskGB, &metadata, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		item.Created = created.UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (c *Client) UpsertSandbox(ctx context.Context, sb Sandbox) {
	metadata, _ := json.Marshal(sb.Metadata)
	var apiKeyID any
	if sb.APIKeyID != "" {
		apiKeyID = sb.APIKeyID
	}
	_, err := c.pool.Exec(ctx, `INSERT INTO sandboxes (id,user_id,api_key_id,name,state,billing_model,vcpus,memory_mb,disk_gb,internet,metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO UPDATE SET user_id=EXCLUDED.user_id,api_key_id=EXCLUDED.api_key_id,name=EXCLUDED.name,state=EXCLUDED.state,billing_model=EXCLUDED.billing_model,vcpus=EXCLUDED.vcpus,memory_mb=EXCLUDED.memory_mb,disk_gb=EXCLUDED.disk_gb,internet=EXCLUDED.internet,metadata=EXCLUDED.metadata,updated_at=now()`, sb.ID, sb.UserID, apiKeyID, sb.Name, sb.State, sb.BillingModel, sb.VCPUs, sb.MemoryMB, sb.DiskGB, sb.Internet, metadata)
	if err != nil {
		slog.Warn("sandbox upsert failed", "err", err)
	}
}

// InsertSandbox creates the durable scheduling row before any worker capacity
// is reserved or VM boot is attempted.
func (c *Client) InsertSandbox(ctx context.Context, sb Sandbox) error {
	metadata, err := json.Marshal(sb.Metadata)
	if err != nil {
		return fmt.Errorf("marshal sandbox metadata: %w", err)
	}
	var apiKeyID any
	if sb.APIKeyID != "" {
		apiKeyID = sb.APIKeyID
	}
	_, err = c.pool.Exec(ctx, `
		INSERT INTO sandboxes (
			id,user_id,api_key_id,name,state,billing_model,
			vcpus,memory_mb,disk_gb,internet,metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		sb.ID,
		sb.UserID,
		apiKeyID,
		sb.Name,
		sb.State,
		sb.BillingModel,
		sb.VCPUs,
		sb.MemoryMB,
		sb.DiskGB,
		sb.Internet,
		metadata,
	)
	if err != nil {
		return fmt.Errorf("insert sandbox: %w", err)
	}
	return nil
}

func (c *Client) UpdateSandboxState(ctx context.Context, id, state string) {
	var err error
	switch state {
	case "paused":
		// Capture pause time for billing/runtime; keep last_used_at as last activity.
		_, err = c.pool.Exec(ctx, `UPDATE sandboxes SET state=$2,updated_at=now(),paused_at=now(),expires_at=now()+interval '7 days' WHERE id=$1`, id, state)
	case "active":
		_, err = c.pool.Exec(ctx, `UPDATE sandboxes SET state=$2,updated_at=now(),last_used_at=now(),paused_at=NULL,expires_at=NULL WHERE id=$1`, id, state)
	case "destroyed", "destroying", "error":
		// Stamp last_used_at so dashboard duration (created → end) is non-zero.
		_, err = c.pool.Exec(ctx, `UPDATE sandboxes SET state=$2,updated_at=now(),last_used_at=now(),expires_at=NULL WHERE id=$1`, id, state)
	default:
		_, err = c.pool.Exec(ctx, `UPDATE sandboxes SET state=$2,updated_at=now() WHERE id=$1`, id, state)
	}
	if err != nil {
		slog.Warn("sandbox state update failed", "err", err)
	}
}

type SandboxRun struct {
	ID         string `json:"id"`
	SandboxID  string `json:"sandbox_id"`
	UserID     string `json:"user_id"`
	Kind       string `json:"kind"`
	Language   string `json:"language"`
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	Status     string `json:"status"`
	DurationMs int    `json:"duration_ms"`
	StartedAt  string `json:"started_at"`
}

func (c *Client) InsertSandboxRun(ctx context.Context, run SandboxRun) {
	_, err := c.pool.Exec(ctx, `INSERT INTO sandbox_runs (id,sandbox_id,user_id,kind,language,command,exit_code,status,duration_ms,started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, run.ID, run.SandboxID, run.UserID, run.Kind, run.Language, run.Command, run.ExitCode, run.Status, run.DurationMs, run.StartedAt)
	if err != nil {
		slog.Warn("sandbox run insert failed", "err", err)
	}
}

type SandboxLog struct {
	SandboxID string `json:"sandbox_id"`
	RunID     string `json:"run_id,omitempty"`
	UserID    string `json:"user_id"`
	Stream    string `json:"stream"`
	Level     string `json:"level,omitempty"`
	Language  string `json:"language,omitempty"`
	Content   string `json:"content"`
}

func (c *Client) InsertSandboxLog(ctx context.Context, entry SandboxLog) {
	var runID any
	if entry.RunID != "" {
		runID = entry.RunID
	}
	_, err := c.pool.Exec(ctx, `INSERT INTO sandbox_logs (sandbox_id,run_id,user_id,stream,level,language,content) VALUES ($1,$2,$3,$4,$5,$6,$7)`, entry.SandboxID, runID, entry.UserID, entry.Stream, entry.Level, entry.Language, entry.Content)
	if err != nil {
		slog.Warn("sandbox log insert failed", "err", err)
	}
}

type UsageMeter struct {
	UserID        string
	SandboxID     string
	BillingModel  string
	Bucket        string
	VCPUSeconds   float64
	RAMGBSeconds  float64
	DiskGBSeconds float64
}

func (c *Client) AccrueUsageMeter(ctx context.Context, meter UsageMeter) {
	_, err := c.pool.Exec(ctx, `INSERT INTO usage_meters (user_id,sandbox_id,billing_model,bucket,vcpu_seconds,ram_gb_seconds,disk_gb_seconds) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (sandbox_id,bucket) DO UPDATE SET vcpu_seconds=usage_meters.vcpu_seconds+EXCLUDED.vcpu_seconds,ram_gb_seconds=usage_meters.ram_gb_seconds+EXCLUDED.ram_gb_seconds,disk_gb_seconds=usage_meters.disk_gb_seconds+EXCLUDED.disk_gb_seconds`, meter.UserID, meter.SandboxID, meter.BillingModel, meter.Bucket, meter.VCPUSeconds, meter.RAMGBSeconds, meter.DiskGBSeconds)
	if err != nil {
		slog.Warn("usage meter accrue failed", "err", err)
	}
}

type SandboxRef struct {
	ID           string    `json:"id"`
	State        string    `json:"state"`
	UserID       string    `json:"user_id"`
	BillingModel string    `json:"billing_model"`
	VCPUs        int       `json:"vcpus"`
	MemoryMB     int       `json:"memory_mb"`
	DiskGB       int       `json:"disk_gb"`
	Internet     bool      `json:"internet"`
	Created      time.Time `json:"created_at"`
	LastUsed     time.Time `json:"last_used_at"`
}

// GetSandbox fetches one sandbox row by id. ok=false when no row matches
// (the id::text comparison also makes malformed ids a miss, not an error).
func (c *Client) GetSandbox(ctx context.Context, id string) (SandboxRef, bool, error) {
	var ref SandboxRef
	err := c.pool.QueryRow(ctx, `SELECT id::text,state,user_id::text,COALESCE(billing_model,'payg'),COALESCE(vcpus,0),COALESCE(memory_mb,0),COALESCE(disk_gb,0),COALESCE(internet,true),created_at,COALESCE(last_used_at,created_at) FROM sandboxes WHERE id::text=$1`, id).
		Scan(&ref.ID, &ref.State, &ref.UserID, &ref.BillingModel, &ref.VCPUs, &ref.MemoryMB, &ref.DiskGB, &ref.Internet, &ref.Created, &ref.LastUsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SandboxRef{}, false, nil
		}
		return SandboxRef{}, false, fmt.Errorf("get sandbox: %w", err)
	}
	return ref, true, nil
}

func (c *Client) ListSandboxesByState(ctx context.Context, states []string) ([]SandboxRef, error) {
	rows, err := c.pool.Query(ctx, `SELECT id::text,state,user_id::text,COALESCE(billing_model,'payg'),COALESCE(vcpus,0),COALESCE(memory_mb,0),COALESCE(disk_gb,0),COALESCE(internet,true),created_at,COALESCE(last_used_at,created_at) FROM sandboxes WHERE state=ANY($1)`, states)
	if err != nil {
		return nil, fmt.Errorf("list sandboxes by state: %w", err)
	}
	defer rows.Close()
	refs := make([]SandboxRef, 0)
	for rows.Next() {
		var ref SandboxRef
		if err := rows.Scan(&ref.ID, &ref.State, &ref.UserID, &ref.BillingModel, &ref.VCPUs, &ref.MemoryMB, &ref.DiskGB, &ref.Internet, &ref.Created, &ref.LastUsed); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
