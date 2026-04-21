package queue

import (
	"backend/internal/executor"
	"context"
	"errors"
)

type JobResult struct {
	Result executor.ExecutionResult
	Err    error
}

type Job struct {
	Code     string
	Language string
	Ctx      context.Context
	ResultCh chan JobResult
}

type JobQueue struct {
	executor executor.Executor
	jobs     chan Job // these are the lists of jobs which will get added here core feature anyhow wed
	workers  int
}

func NewJobQueue(exec executor.Executor, maxWorkers int) *JobQueue {
	return &JobQueue{
		executor: exec,
		jobs:     make(chan Job, 100), //buffered channel
		workers:  maxWorkers,
	}
}

func (q *JobQueue) worker() {
	// reading jobs from channel this is just task implementation
	for job := range q.jobs {
		result, err := q.executor.Execute(job.Ctx, job.Code, job.Language)
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

func (q *JobQueue) Depth() int { return len(q.jobs) }

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
