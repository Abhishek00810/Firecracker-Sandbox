package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"backend/internal/orchestrator"

	"github.com/jackc/pgx/v5"
)

var _ orchestrator.Store = (*Client)(nil)

func (c *Client) RegisterWorker(ctx context.Context, worker orchestrator.WorkerRegistration, heartbeatAt time.Time) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO worker_hosts (
			id, endpoint, pool, status, draining,
			allocatable_vcpus, allocatable_memory_mb, allocatable_disk_gb,
			max_sandboxes, last_heartbeat_at, updated_at
		)
		VALUES ($1,$2,$3,'active',false,$4,$5,$6,$7,$8,now())
		ON CONFLICT (id) DO UPDATE SET
			endpoint=EXCLUDED.endpoint,
			pool=EXCLUDED.pool,
			status=CASE WHEN worker_hosts.draining THEN 'draining' ELSE 'active' END,
			allocatable_vcpus=EXCLUDED.allocatable_vcpus,
			allocatable_memory_mb=EXCLUDED.allocatable_memory_mb,
			allocatable_disk_gb=EXCLUDED.allocatable_disk_gb,
			max_sandboxes=EXCLUDED.max_sandboxes,
			last_heartbeat_at=EXCLUDED.last_heartbeat_at,
			updated_at=now()`,
		worker.ID,
		worker.Endpoint,
		worker.Pool,
		worker.AllocatableVCPUs,
		worker.AllocatableMemoryMB,
		worker.AllocatableDiskGB,
		worker.MaxSandboxes,
		heartbeatAt,
	)
	if err != nil {
		return fmt.Errorf("register worker %s: %w", worker.ID, err)
	}
	return nil
}

func (c *Client) RecordHeartbeat(ctx context.Context, workerID string, heartbeatAt time.Time) error {
	tag, err := c.pool.Exec(ctx, `
		UPDATE worker_hosts
		SET last_heartbeat_at=$2,
		    status=CASE WHEN draining THEN 'draining' ELSE 'active' END,
		    updated_at=now()
		WHERE id=$1`,
		workerID,
		heartbeatAt,
	)
	if err != nil {
		return fmt.Errorf("record worker heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return orchestrator.ErrWorkerNotFound
	}
	return nil
}

func (c *Client) ReservePlacement(
	ctx context.Context,
	sandboxID string,
	request orchestrator.PlacementRequest,
	policy orchestrator.PlacementPolicy,
	healthyAfter time.Time,
) (orchestrator.Placement, error) {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return orchestrator.Placement{}, fmt.Errorf("begin placement transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var existingWorkerID *string
	var vcpus, memoryMB, diskGB int
	if err := tx.QueryRow(ctx, `
		SELECT host_id, vcpus, memory_mb, disk_gb
		FROM sandboxes
		WHERE id::text=$1
		FOR UPDATE`,
		sandboxID,
	).Scan(&existingWorkerID, &vcpus, &memoryMB, &diskGB); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orchestrator.Placement{}, orchestrator.ErrSandboxNotFound
		}
		return orchestrator.Placement{}, fmt.Errorf("lock sandbox placement: %w", err)
	}
	if vcpus <= 0 || memoryMB <= 0 || diskGB <= 0 {
		return orchestrator.Placement{}, errors.New("sandbox has invalid resource requirements")
	}

	if existingWorkerID != nil {
		var placement orchestrator.Placement
		placement.SandboxID = sandboxID
		placement.WorkerID = *existingWorkerID
		if err := tx.QueryRow(ctx, `SELECT endpoint FROM worker_hosts WHERE id=$1`, *existingWorkerID).
			Scan(&placement.Endpoint); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return orchestrator.Placement{}, orchestrator.ErrWorkerNotFound
			}
			return orchestrator.Placement{}, fmt.Errorf("read existing placement: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return orchestrator.Placement{}, fmt.Errorf("commit existing placement: %w", err)
		}
		return placement, nil
	}

	var placement orchestrator.Placement
	placement.SandboxID = sandboxID
	err = tx.QueryRow(ctx, `
		SELECT id, endpoint
		FROM worker_hosts
		WHERE pool=$1
		  AND status='active'
		  AND NOT draining
		  AND last_heartbeat_at >= $2
		  AND FLOOR(allocatable_vcpus * $6)-reserved_vcpus >= $3
		  AND FLOOR(allocatable_memory_mb * $7)-reserved_memory_mb >= $4
		  AND allocatable_disk_gb-reserved_disk_gb >= $5
		  AND max_sandboxes-reserved_sandboxes >= 1
		ORDER BY
		  FLOOR(allocatable_memory_mb * $7)-reserved_memory_mb-$4 ASC,
		  FLOOR(allocatable_vcpus * $6)-reserved_vcpus-$3 ASC,
		  id ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1`,
		request.Pool,
		healthyAfter,
		vcpus,
		memoryMB,
		diskGB,
		policy.CPUOvercommitRatio,
		policy.MemoryOvercommitRatio,
	).Scan(&placement.WorkerID, &placement.Endpoint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orchestrator.Placement{}, orchestrator.ErrNoCapacity
		}
		return orchestrator.Placement{}, fmt.Errorf("select worker for placement: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE worker_hosts
		SET reserved_vcpus=reserved_vcpus+$2,
		    reserved_memory_mb=reserved_memory_mb+$3,
		    reserved_disk_gb=reserved_disk_gb+$4,
		    reserved_sandboxes=reserved_sandboxes+1,
		    updated_at=now()
		WHERE id=$1`,
		placement.WorkerID,
		vcpus,
		memoryMB,
		diskGB,
	); err != nil {
		return orchestrator.Placement{}, fmt.Errorf("reserve worker capacity: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sandboxes
		SET host_id=$2, state='provisioning', updated_at=now()
		WHERE id::text=$1`,
		sandboxID,
		placement.WorkerID,
	); err != nil {
		return orchestrator.Placement{}, fmt.Errorf("save sandbox placement: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return orchestrator.Placement{}, fmt.Errorf("commit placement: %w", err)
	}
	return placement, nil
}

func (c *Client) GetPlacement(ctx context.Context, sandboxID string) (orchestrator.Placement, bool, error) {
	var placement orchestrator.Placement
	err := c.pool.QueryRow(ctx, `
		SELECT s.id::text, w.id, w.endpoint
		FROM sandboxes s
		JOIN worker_hosts w ON w.id=s.host_id
		WHERE s.id::text=$1`,
		sandboxID,
	).Scan(&placement.SandboxID, &placement.WorkerID, &placement.Endpoint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orchestrator.Placement{}, false, nil
		}
		return orchestrator.Placement{}, false, fmt.Errorf("get sandbox placement: %w", err)
	}
	return placement, true, nil
}

func (c *Client) UpdatePlacementState(ctx context.Context, sandboxID, workerID string, fromStates []string, toState string) error {
	tag, err := c.pool.Exec(ctx, `
		UPDATE sandboxes
		SET state=$4, updated_at=now()
		WHERE id::text=$1
		  AND host_id=$2
		  AND state=ANY($3)`,
		sandboxID,
		workerID,
		fromStates,
		toState,
	)
	if err != nil {
		return fmt.Errorf("update sandbox placement state: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	var currentState string
	var currentWorkerID *string
	err = c.pool.QueryRow(ctx, `SELECT state, host_id FROM sandboxes WHERE id::text=$1`, sandboxID).
		Scan(&currentState, &currentWorkerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.ErrSandboxNotFound
	}
	if err != nil {
		return fmt.Errorf("read sandbox state after transition conflict: %w", err)
	}
	if currentState == toState && currentWorkerID != nil && *currentWorkerID == workerID {
		return nil
	}
	return orchestrator.ErrInvalidState
}

func (c *Client) ReleasePlacement(ctx context.Context, sandboxID, finalState string) error {
	return c.releasePlacement(ctx, sandboxID, "", finalState)
}

func (c *Client) ReleaseWorkerPlacement(ctx context.Context, sandboxID, workerID, finalState string) error {
	return c.releasePlacement(ctx, sandboxID, workerID, finalState)
}

func (c *Client) releasePlacement(ctx context.Context, sandboxID, expectedWorkerID, finalState string) error {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin placement release: %w", err)
	}
	defer tx.Rollback(ctx)

	var workerID *string
	var vcpus, memoryMB, diskGB int
	var currentState string
	err = tx.QueryRow(ctx, `
		SELECT host_id, vcpus, memory_mb, disk_gb, state
		FROM sandboxes
		WHERE id::text=$1
		FOR UPDATE`,
		sandboxID,
	).Scan(&workerID, &vcpus, &memoryMB, &diskGB, &currentState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orchestrator.ErrSandboxNotFound
		}
		return fmt.Errorf("lock sandbox placement for release: %w", err)
	}
	if workerID == nil {
		if expectedWorkerID != "" {
			if currentState == finalState {
				return tx.Commit(ctx)
			}
			return orchestrator.ErrInvalidState
		}
		if finalState != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE sandboxes
				SET state=$2, updated_at=now()
				WHERE id::text=$1`,
				sandboxID,
				finalState,
			); err != nil {
				return fmt.Errorf("finalize unplaced sandbox: %w", err)
			}
		}
		return tx.Commit(ctx)
	}
	if expectedWorkerID != "" && *workerID != expectedWorkerID {
		return orchestrator.ErrInvalidState
	}

	if _, err := tx.Exec(ctx, `
		UPDATE worker_hosts
		SET reserved_vcpus=GREATEST(0,reserved_vcpus-$2),
		    reserved_memory_mb=GREATEST(0,reserved_memory_mb-$3),
		    reserved_disk_gb=GREATEST(0,reserved_disk_gb-$4),
		    reserved_sandboxes=GREATEST(0,reserved_sandboxes-1),
		    updated_at=now()
		WHERE id=$1`,
		*workerID,
		vcpus,
		memoryMB,
		diskGB,
	); err != nil {
		return fmt.Errorf("release worker capacity: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sandboxes
		SET host_id=NULL,
		    state=CASE WHEN $2='' THEN state ELSE $2 END,
		    updated_at=now()
		WHERE id::text=$1`,
		sandboxID,
		finalState,
	); err != nil {
		return fmt.Errorf("clear sandbox placement: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit placement release: %w", err)
	}
	return nil
}
