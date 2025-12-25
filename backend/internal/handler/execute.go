package handler

import (
	"backend/internal/executor"
	"backend/internal/queue"
	"encoding/json"
	"net/http"
)

type ExecuteRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
}

type ExecuteResponse struct {
	Output executor.ExecutionResult `json:"output"`
	Error  string                   `json:"error,omitempty"`
	Status string                   `json:"status"`
}

func ExecuteHandler(JobQueue *queue.JobQueue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ExecuteRequest
		err := json.NewDecoder(r.Body).Decode(&req)

		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
		}

		resultCh, err := JobQueue.Submit(r.Context(), req.Code, req.Language)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}

		result := <-resultCh

		resp := ExecuteResponse{
			Output: result.Result,
			Status: "success",
		}

		if result.Err != nil {
			resp.Error = result.Err.Error()
			resp.Status = "error"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
