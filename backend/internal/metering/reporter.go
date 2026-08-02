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
	input    chan reporterCommand
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

type reporterCommand struct {
	sample *Sample
	flush  chan error
}

func NewReporter(client *Client, workerID string) *Reporter {
	r := &Reporter{
		client:   client,
		workerID: workerID,
		input:    make(chan reporterCommand, defaultQueueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go r.run()
	return r
}

func (r *Reporter) Record(sample Sample) {
	sample.WorkerID = r.workerID
	r.input <- reporterCommand{sample: &sample}
}

// Flush waits until every sample recorded before this call has been submitted.
// The command shares the sample queue, so channel ordering provides the barrier.
func (r *Reporter) Flush(ctx context.Context) error {
	result := make(chan error, 1)
	select {
	case r.input <- reporterCommand{flush: result}:
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-result:
		return err
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
		case command := <-r.input:
			if command.sample != nil {
				pending = append(pending, *command.sample)
				if len(pending) >= maxBatchSize {
					pending, _ = r.flush(pending)
				}
			}
			if command.flush != nil {
				var err error
				pending, err = r.flushAll(pending)
				command.flush <- err
			}
		case <-ticker.C:
			pending, _ = r.flush(pending)
		case <-r.stop:
			for {
				select {
				case command := <-r.input:
					if command.sample != nil {
						pending = append(pending, *command.sample)
					}
					if command.flush != nil {
						var err error
						pending, err = r.flushAll(pending)
						command.flush <- err
					}
				default:
					r.flushUntilEmpty(pending)
					return
				}
			}
		}
	}
}

func (r *Reporter) flush(pending []Sample) ([]Sample, error) {
	if len(pending) == 0 {
		return pending, nil
	}
	count := min(len(pending), maxBatchSize)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := r.client.Submit(ctx, pending[:count])
	cancel()
	if err != nil {
		slog.Warn("usage batch delivery failed; retaining batch for retry", "samples", count, "err", err)
		return pending, err
	}
	copy(pending, pending[count:])
	return pending[:len(pending)-count], nil
}

func (r *Reporter) flushAll(pending []Sample) ([]Sample, error) {
	for len(pending) > 0 {
		var err error
		pending, err = r.flush(pending)
		if err != nil {
			return pending, err
		}
	}
	return pending, nil
}

func (r *Reporter) flushUntilEmpty(pending []Sample) {
	for len(pending) > 0 {
		var err error
		pending, err = r.flush(pending)
		if err != nil {
			time.Sleep(time.Second)
		}
	}
}
