package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// manifest persists the set of PAUSED sessions to disk so they survive a backend
// process restart (deploy, reboot) — the snapshot + writable disk are already on disk;
// the manifest is the index that lets a fresh process find and resume them.
//
// Scope (v1): this covers process restart / reboot, where the disk survives. It does NOT
// survive local-NVMe loss (EC2 stop-start / host failure) — that needs durable storage
// (S3/Blob), a later phase. Records pointing at missing files are dropped on recovery.

type pausedRecord struct {
	ID               string            `json:"id"`
	UserID           string            `json:"user_id"`
	BillingModel     string            `json:"billing_model"`
	LegacyTier       string            `json:"tier,omitempty"` // read-only compatibility for pre-PAYG manifests
	VCPUs            int               `json:"vcpus"`
	MemoryMB         int               `json:"memory_mb"`
	DiskGB           int               `json:"disk_gb"`
	Env              map[string]string `json:"env,omitempty"`
	Internet         bool              `json:"internet"`
	IdleTimeoutNs    int64             `json:"idle_timeout_ns"`
	MaxLifetimeNs    int64             `json:"max_lifetime_ns"`
	CreatedAt        time.Time         `json:"created_at"`
	PausedAt         time.Time         `json:"paused_at"`
	SnapPath         string            `json:"snap_path"`
	MemPath          string            `json:"mem_path"`
	WritableDiskPath string            `json:"writable_disk_path"`
	VsockPathAtPause string            `json:"vsock_path_at_pause"`
	TapNameAtPause   string            `json:"tap_name_at_pause"`
}

func recordFromSession(s *Session) pausedRecord {
	return pausedRecord{
		ID:               s.ID,
		UserID:           s.UserID,
		BillingModel:     s.BillingModel,
		VCPUs:            s.VCPUs,
		MemoryMB:         s.MemoryMB,
		DiskGB:           s.DiskGB,
		Env:              s.Env,
		Internet:         s.Internet,
		IdleTimeoutNs:    int64(s.IdleTimeout),
		MaxLifetimeNs:    int64(s.MaxLifetime),
		CreatedAt:        s.CreatedAt,
		PausedAt:         s.PausedAt,
		SnapPath:         s.SnapPath,
		MemPath:          s.MemPath,
		WritableDiskPath: s.WritableDiskPath,
		VsockPathAtPause: s.VsockPathAtPause,
		TapNameAtPause:   s.TapNameAtPause,
	}
}

func (r pausedRecord) toSession() *Session {
	billingModel := r.BillingModel
	if billingModel == "" {
		billingModel = r.LegacyTier
	}
	if billingModel == "" {
		billingModel = "payg"
	}
	return &Session{
		ID:               r.ID,
		UserID:           r.UserID,
		BillingModel:     billingModel,
		VCPUs:            r.VCPUs,
		MemoryMB:         r.MemoryMB,
		DiskGB:           r.DiskGB,
		Env:              r.Env,
		Internet:         r.Internet,
		IdleTimeout:      time.Duration(r.IdleTimeoutNs),
		MaxLifetime:      time.Duration(r.MaxLifetimeNs),
		CreatedAt:        r.CreatedAt,
		LastUsed:         r.PausedAt,
		State:            StatePaused,
		PausedAt:         r.PausedAt,
		SnapPath:         r.SnapPath,
		MemPath:          r.MemPath,
		WritableDiskPath: r.WritableDiskPath,
		VsockPathAtPause: r.VsockPathAtPause,
		TapNameAtPause:   r.TapNameAtPause,
	}
}

// writeManifest atomically rewrites the manifest with the given paused sessions.
func writeManifest(path string, paused []*Session) error {
	records := make([]pausedRecord, 0, len(paused))
	for _, s := range paused {
		records = append(records, recordFromSession(s))
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir manifest dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write manifest tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename manifest: %w", err)
	}
	return nil
}

// readManifest loads paused-session records. A missing file is not an error (no paused
// sessions). Records whose snapshot/disk files no longer exist (e.g. NVMe wiped on a
// stop-start) are dropped so we never try to resume a sandbox whose state is gone.
func readManifest(path string) ([]*Session, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var records []pausedRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	sessions := make([]*Session, 0, len(records))
	for _, r := range records {
		if !fileExists(r.SnapPath) || !fileExists(r.MemPath) || !fileExists(r.WritableDiskPath) {
			continue // state is gone (host/disk loss) — cannot resume; drop it
		}
		sessions = append(sessions, r.toSession())
	}
	return sessions, nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
