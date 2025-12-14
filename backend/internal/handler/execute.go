package handler

import (
	"backend/internal/executor"
	"encoding/json"
	"net/http"
)

type ExecuteRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
}

type ExecuteResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
	Status string `json:"status"`
}

func ExecuteHandler(dockerExec *executor.DockerExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ExecuteRequest

		err := json.NewDecoder(r.Body).Decode(&req)

		if err != nil {
			http.Error(w, "Invalid Request", http.StatusBadRequest)
		}

		output, err := dockerExec.Execute(req.Code, req.Language)

		resp := ExecuteResponse{
			Output: output,
			Status: "success",
		}

		if err != nil {
			resp.Error = err.Error()
			resp.Status = "error"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)

	}
}
