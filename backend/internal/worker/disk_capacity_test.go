package worker

import "testing"

func TestCalculateDiskCapacityUsesFilesystemAndReserve(t *testing.T) {
	got := calculateDiskCapacityGB(2048, 1900, 0, 0, 100)
	if got != 1800 {
		t.Fatalf("capacity=%d want=1800", got)
	}
}

func TestCalculateDiskCapacityDoesNotDoubleCountReservations(t *testing.T) {
	got := calculateDiskCapacityGB(2048, 1780, 120, 0, 100)
	if got != 1800 {
		t.Fatalf("capacity=%d want=1800", got)
	}
}

func TestCalculateDiskCapacityHonorsOptionalCap(t *testing.T) {
	got := calculateDiskCapacityGB(2048, 1900, 0, 1500, 100)
	if got != 1500 {
		t.Fatalf("capacity=%d want=1500", got)
	}
}

func TestCalculateDiskCapacityUsesAutomaticReserve(t *testing.T) {
	got := calculateDiskCapacityGB(2000, 2000, 0, 0, 0)
	if got != 1900 {
		t.Fatalf("capacity=%d want=1900", got)
	}
}
