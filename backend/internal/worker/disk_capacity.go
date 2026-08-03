package worker

import (
	"fmt"
	"syscall"
)

const bytesPerGiB = uint64(1 << 30)

// HostDiskCapacity derives schedulable disk from the filesystem containing the
// worker root. capGB is an optional operator ceiling; reserveGB=0 keeps 5% of
// the filesystem (at least 10 GiB) unavailable to sandboxes.
type HostDiskCapacity struct {
	path      string
	capGB     int
	reserveGB int
	statfs    func(string, *syscall.Statfs_t) error
}

func NewHostDiskCapacity(path string, capGB, reserveGB int) *HostDiskCapacity {
	return &HostDiskCapacity{
		path: path, capGB: capGB, reserveGB: reserveGB, statfs: syscall.Statfs,
	}
}

func (d *HostDiskCapacity) CapacityGB(reservedDiskGB int) (int, error) {
	var stat syscall.Statfs_t
	if err := d.statfs(d.path, &stat); err != nil {
		return 0, fmt.Errorf("stat worker filesystem %q: %w", d.path, err)
	}
	totalGB := int(uint64(stat.Blocks) * uint64(stat.Bsize) / bytesPerGiB)
	availableGB := int(uint64(stat.Bavail) * uint64(stat.Bsize) / bytesPerGiB)
	return calculateDiskCapacityGB(totalGB, availableGB, reservedDiskGB, d.capGB, d.reserveGB), nil
}

func calculateDiskCapacityGB(totalGB, availableGB, reservedGB, capGB, reserveGB int) int {
	if totalGB <= 0 || availableGB < 0 {
		return 0
	}
	if reserveGB <= 0 {
		reserveGB = totalGB / 20
		if reserveGB < 10 {
			reserveGB = 10
		}
	}

	// available+reserved avoids subtracting the same sandbox quota twice:
	// Admission later computes allocatable-reserved for the remaining capacity.
	byFreeSpace := availableGB + reservedGB - reserveGB
	byTotalSize := totalGB - reserveGB
	capacity := min(byFreeSpace, byTotalSize)
	if capGB > 0 {
		capacity = min(capacity, capGB)
	}
	if capacity < 0 {
		return 0
	}
	return capacity
}
