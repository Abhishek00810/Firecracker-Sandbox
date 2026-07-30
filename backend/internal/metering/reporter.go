package metering

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultQueueSize = 10000
	maxBatchSize     = 500
)

// Reporter keeps metering network I/O off the session ticker. Failed batches
// remain at the head of the in-memory queue and are retried with backoff.
type Reporter struct {
	client   *Client
	workerID string
	input    chan Sample
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func NewReporter(client *Client, workerID string) *Reporter {
	r := &Reporter{
		client:   client,
		workerID: workerID,
		input:    make(chan Sample, defaultQueueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go r.run()
	return r
}

func (r *Reporter) Record(sample Sample) {
	sample.WorkerID = r.workerID
	r.input <- sample
}

func (r *Reporter) Shutdown(ctx context.Context) error {
	r.once.Do(func() { close(r.stop) })
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Reporter) run() {
	defer close(r.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	pending := make([]Sample, 0, maxBatchSize)
	for {
		select {
		case sample := <-r.input:
			pending = append(pending, sample)
			if len(pending) >= maxBatchSize {
				pending = r.flush(pending)
			}
		case <-ticker.C:
			pending = r.flush(pending)
		case <-r.stop:
			for {
				select {
				case sample := <-r.input:
					pending = append(pending, sample)
				default:
					r.flushUntilEmpty(pending)
					return
				}
			}
		}
	}
}

func (r *Reporter) flush(pending []Sample) []Sample {
	if len(pending) == 0 {
		return pending
	}
	count := min(len(pending), maxBatchSize)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := r.client.Submit(ctx, pending[:count])
	cancel()
	if err != nil {
		slog.Warn("usage batch delivery failed; retaining batch for retry", "samples", count, "err", err)
		time.Sleep(time.Second)
		return pending
	}
	copy(pending, pending[count:])
	return pending[:len(pending)-count]
}

func (r *Reporter) flushUntilEmpty(pending []Sample) {
	for len(pending) > 0 {
		pending = r.flush(pending)
	}
}
