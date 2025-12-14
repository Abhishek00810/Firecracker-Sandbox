package main

import (
	"backend/internal/executor"
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
	_, err := executor.NewDockerExecutor(ctx)

	if err != nil {
		log.Fatal("Docker is required but not available:", err)
	} else {
		log.Printf("Docker connected successfully!!")
	}

	http.HandleFunc("/health", healthHandler)

	port := ":8080"

	log.Printf("Server is running on Port 8080 huh!!")

	err = http.ListenAndServe(port, nil)

	if err != nil {
		log.Fatalf("error in serving the API: %v", err)
	}

}
