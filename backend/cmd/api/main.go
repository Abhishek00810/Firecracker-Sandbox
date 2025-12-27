package main

import (
	"backend/internal/executor"
	"backend/internal/handler"
	"backend/internal/queue"
	"context"
	"encoding/json"
	"log"
	"net/http"
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

	ctx := context.Background()
	dockerExec, err := executor.NewDockerExecutor(ctx)

	if err != nil {
		log.Fatal("Docker is required but not available:", err)
	} else {
		log.Printf("Docker connected successfully!!")
	}
	dockerExec.EnsureImage(ctx, "python:alpine") // startup time
	JobQueue := queue.NewJobQueue(dockerExec, 10)
	JobQueue.Start()

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", handler.ExecuteHandler(JobQueue))

	port := ":8080"

	log.Printf("Server is running on Port 8080 huh!!")

	err = http.ListenAndServe(port, nil)

	if err != nil {
		log.Fatalf("error in serving the API: %v", err)
	}

}
