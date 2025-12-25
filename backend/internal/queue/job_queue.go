package queue

import "backend/internal/executor"

type Job struct {
	Code     string
	Language string

	//Output channels
	ResultCh chan executor.ExecutionResult
	ErrorCh  chan error
}

type JobQueue struct {
	executor *executor.DockerExecutor
	jobs     chan Job
	workers  int
}
