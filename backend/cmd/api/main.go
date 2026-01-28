package main

import (
	"backend/internal/executor/firecracker"
	"backend/internal/handler"
	"backend/internal/metrics"
	"backend/internal/queue"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type HealthResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := HealthResponse{
		Status:  "ok",
		Message: "Server is healthy and is rocking!!!",
	}

	json.NewEncoder(w).Encode(resp)
}

func MetricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	m := metrics.GetMetrics()
	json.NewEncoder(w).Encode(m)
}

func main() {
	// Setup Firecracker executor
	socketDir := filepath.Join(os.TempDir(), "fc-sockets")
	assetsPath := os.Getenv("ASSETS_PATH")
	if assetsPath == "" {
		assetsPath = "/app/assets" // Default for Docker
	}

	// Create socket directory if it doesn't exist
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		log.Fatalf("Failed to create socket directory: %v", err)
	}

	vmManager := firecracker.NewFirecrackerManager(socketDir, assetsPath)
	firecrackerExec := firecracker.NewFirecrackerExecutor(vmManager)

	config := firecracker.VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		Timeout:    30 * time.Second,
		KernelPath: filepath.Join(assetsPath, "kernel/vmlinux"),
		RootfsPath: filepath.Join(assetsPath, "rootfs/rootfs-alpine.ext4"),
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off init=/usr/local/bin/guest-agent",
	}

	//  pool (pre-boots 3 VMs)
	pool := firecracker.NewVMPool(3, config, vmManager)
	log.Printf("Firecracker executor initialized successfully!")
	firecrackerExec.Pool = pool                        // ← Connect pool
	JobQueue := queue.NewJobQueue(firecrackerExec, 10) // 10 workers
	JobQueue.Start()
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", handler.ExecuteHandler(JobQueue))
	http.HandleFunc("/metrics", MetricsHandler)

	port := ":8080"

	log.Printf("Server is running on Port 8080 huh!!")

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("error in serving the API: %v", err)
	}
}
