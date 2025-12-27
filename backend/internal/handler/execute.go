package handler

import (
	"backend/internal/executor"
	"backend/internal/queue"
	"encoding/json"
	"fmt"
	"log"
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
			return
		}

		result := <-resultCh

		if result.Result.TerminationReason == "timeout" {
			// Log this - YOUR system killed it
			log.Printf("[TIMEOUT] Execution timed out - Duration: %.2fs", result.Result.Duration)
		} else if result.Result.TerminationReason == "oom_kill" {
			// Don't log
		} else if result.Result.ExitCode == 137 && result.Result.TerminationReason != "oom_kill" {
			// Unexpected 137 - investigate!
			log.Printf("[ALERT] Unexpected exit code 137 - Investigate!")
		}
		resp := ExecuteResponse{
			Output: result.Result,
			Status: "success",
		}

		if result.Err != nil {
			resp.Error = result.Err.Error()
			resp.Status = "error"
		} else if result.Result.ExitCode != 0 {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("Execution failed with exit code %d", result.Result.ExitCode)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
