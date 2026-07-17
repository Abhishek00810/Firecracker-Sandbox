// Package postgres is the Postgres-backed adapter for the control-plane ports
// (execution.SandboxStore for now). It is a driven adapter: the application
// core never imports it. Migrating the database is either a DATABASE_URL change
// (any Postgres-compatible host: RDS/Aurora Postgres, Supabase, Neon, PlanetScale
// for Postgres) or a sibling adapter implementing the same port (a different
// engine such as MySQL). Queries use standard SQL to keep that path short.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/application/execution"
)

// Store holds a pooled Postgres connection and implements the control-plane's
// storage ports.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool to Postgres and verifies connectivity. dsn is a
// standard connection URL, so the host is entirely a deployment concern.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// GetAllocation returns which worker owns a sandbox for a tenant. host_id is the
// durable worker assignment; a NULL host_id means the sandbox exists but has no
// worker yet, which the execution service treats as "no worker assignment".
func (s *Store) GetAllocation(ctx context.Context, tenantID, sandboxID string) (execution.Allocation, error) {
	var (
		alloc    execution.Allocation
		workerID *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, user_id::text, host_id
		FROM sandboxes
		WHERE id = $1::uuid AND user_id = $2::uuid AND state <> 'destroyed'
		LIMIT 1`, sandboxID, tenantID).Scan(&alloc.SandboxID, &alloc.TenantID, &workerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Allocation{}, execution.ErrSandboxNotFound
	}
	if err != nil {
		return execution.Allocation{}, fmt.Errorf("get allocation: %w", err)
	}
	if workerID != nil {
		alloc.WorkerID = *workerID
	}
	return alloc, nil
}

// compile-time proof this adapter satisfies the port.
var _ execution.SandboxStore = (*Store)(nil)
