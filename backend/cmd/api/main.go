package main

import (
	"backend/internal/executor/firecracker"
	"backend/internal/handler"
	"backend/internal/queue"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

func main() {
	// Setup Firecracker executor
	socketDir := filepath.Join(os.TempDir(), "fc-sockets")
	assetsPath := "/Users/abhishekdadwal/nothing/sandbox_env/assets"

	// Create socket directory if it doesn't exist
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		log.Fatalf("Failed to create socket directory: %v", err)
	}

	vmManager := firecracker.NewFirecrackerManager(socketDir, assetsPath)
	firecrackerExec := firecracker.NewFirecrackerExecutor(vmManager)

	log.Printf("Firecracker executor initialized successfully!")

	JobQueue := queue.NewJobQueue(firecrackerExec, 10)
	JobQueue.Start()

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", handler.ExecuteHandler(JobQueue))

	port := ":8080"

	log.Printf("Server is running on Port 8080 huh!!")

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("error in serving the API: %v", err)
	}
}
