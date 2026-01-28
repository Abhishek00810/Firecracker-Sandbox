package metrics

import "sync"

type Metrics struct {
	mu sync.Mutex

	//execution metrics
	TotalExecutions int64
	SuccessCount    int64
	FailureCount    int64
	TotalDuration   float64
}

var globalMetrics = &Metrics{
	TotalExecutions: 0,
	SuccessCount:    0,
	FailureCount:    0,
	TotalDuration:   0.0,
}

func RecordExecution(duration float64, success bool) {
	globalMetrics.mu.Lock()
	defer globalMetrics.mu.Unlock()

	globalMetrics.TotalExecutions++
	globalMetrics.TotalDuration += duration

	if success {
		globalMetrics.SuccessCount++
	} else {
		globalMetrics.FailureCount++
	}
}
func GetMetrics() *Metrics {
	globalMetrics.mu.Lock()
	defer globalMetrics.mu.Unlock()
	copy := Metrics{
		TotalExecutions: globalMetrics.TotalExecutions,
		SuccessCount:    globalMetrics.SuccessCount,
		FailureCount:    globalMetrics.FailureCount,
		TotalDuration:   globalMetrics.TotalDuration,
	}
	return &copy
}
