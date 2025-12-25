package queue

import (
	"backend/internal/executor"
	"context"
	"errors"
	"time"
)

type JobResult struct {
	Result executor.ExecutionResult
	Err    error
}

type Job struct {
	Code     string
	Language string
	//Output channel
	Ctx      context.Context
	ResultCh chan JobResult
}

type JobQueue struct {
	executor *executor.DockerExecutor
	jobs     chan Job // these are the lists of jobs which will get added here core feature
	workers  int
}

func NewJobQueue(exec *executor.DockerExecutor, maxWorkers int) *JobQueue {
	return &JobQueue{
		executor: exec,
		jobs:     make(chan Job, 100), //buffered channel
		workers:  maxWorkers,
	}
}

func (q *JobQueue) worker() {
	// reading jobs from channel this is just task implementation
	for job := range q.jobs {

		ctx, cancel := context.WithTimeout(job.Ctx, 12*time.Second)
		defer cancel()
		result, err := q.executor.Execute(ctx, job.Code, job.Language)
		job.ResultCh <- JobResult{
			Result: result,
			Err:    err,
		}
	}
}

func (q *JobQueue) Start() {
	// spawn qworkers number of goroutines
	for i := 0; i < q.workers; i++ {
		go q.worker() // start worker goroutine
	}
}

func (q *JobQueue) Submit(ctx context.Context, code, language string) (chan JobResult, error) {
	resultCh := make(chan JobResult, 1)

	job := Job{
		Code:     code,
		Language: language,
		Ctx:      ctx, //caller's context
		ResultCh: resultCh,
	}

	select {
	case q.jobs <- job:
		return resultCh, nil
	default:
		return nil, errors.New("queue full, try again later")
	}
}
