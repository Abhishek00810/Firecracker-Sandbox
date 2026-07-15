package metrics

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

type ErrorType string

const (
	ErrorNone    ErrorType = "none"
	ErrorTimeout ErrorType = "timeout"
	ErrorOOM     ErrorType = "oom"
	ErrorRuntime ErrorType = "runtime"
	ErrorSystem  ErrorType = "system"
)

type metrics struct {
	mu              sync.Mutex
	totalExecutions int64
	successCount    int64
	failureCount    int64
	timeoutCount    int64
	oomCount        int64
	runtimeErrCount int64
	systemErrCount  int64
	ring            [1000]float64
	ringHead        int
	ringFull        bool
}

var globalMetrics = &metrics{}
var activeExecutions atomic.Int64

type MetricsSnapshot struct {
	TotalExecutions  int64   `json:"total_executions"`
	SuccessCount     int64   `json:"success_count"`
	FailureCount     int64   `json:"failure_count"`
	SuccessRate      float64 `json:"success_rate"`
	AvgDuration      float64 `json:"avg_duration_seconds"`
	P50Duration      float64 `json:"p50_duration_seconds"`
	P95Duration      float64 `json:"p95_duration_seconds"`
	P99Duration      float64 `json:"p99_duration_seconds"`
	TimeoutCount     int64   `json:"timeout_count"`
	OOMCount         int64   `json:"oom_count"`
	RuntimeErrCount  int64   `json:"runtime_err_count"`
	SystemErrCount   int64   `json:"system_err_count"`
	ActiveExecutions int64   `json:"active_executions"`
	// Populated by caller:
	QueueDepth      int `json:"queue_depth"`
	VMPoolAvailable int `json:"vm_pool_available"`
	VMPoolInUse     int `json:"vm_pool_in_use"`
}

func RecordExecutionStart() {
	activeExecutions.Add(1)
}

func RecordExecutionEnd(duration float64, errType ErrorType) {
	activeExecutions.Add(-1)

	globalMetrics.mu.Lock()
	defer globalMetrics.mu.Unlock()

	globalMetrics.totalExecutions++
	globalMetrics.ring[globalMetrics.ringHead] = duration
	globalMetrics.ringHead = (globalMetrics.ringHead + 1) % 1000
	if globalMetrics.ringHead == 0 {
		globalMetrics.ringFull = true
	}

	switch errType {
	case ErrorNone:
		globalMetrics.successCount++
	case ErrorTimeout:
		globalMetrics.failureCount++
		globalMetrics.timeoutCount++
	case ErrorOOM:
		globalMetrics.failureCount++
		globalMetrics.oomCount++
	case ErrorRuntime:
		globalMetrics.failureCount++
		globalMetrics.runtimeErrCount++
	case ErrorSystem:
		globalMetrics.failureCount++
		globalMetrics.systemErrCount++
	}
}

func GetSnapshot() MetricsSnapshot {
	globalMetrics.mu.Lock()

	snap := MetricsSnapshot{
		TotalExecutions:  globalMetrics.totalExecutions,
		SuccessCount:     globalMetrics.successCount,
		FailureCount:     globalMetrics.failureCount,
		TimeoutCount:     globalMetrics.timeoutCount,
		OOMCount:         globalMetrics.oomCount,
		RuntimeErrCount:  globalMetrics.runtimeErrCount,
		SystemErrCount:   globalMetrics.systemErrCount,
		ActiveExecutions: activeExecutions.Load(),
	}

	var n int
	var durations []float64
	if globalMetrics.ringFull {
		n = 1000
		durations = make([]float64, 1000)
		copy(durations, globalMetrics.ring[:])
	} else {
		n = globalMetrics.ringHead
		durations = make([]float64, n)
		copy(durations, globalMetrics.ring[:n])
	}

	globalMetrics.mu.Unlock()

	if snap.TotalExecutions > 0 {
		snap.SuccessRate = float64(snap.SuccessCount) / float64(snap.TotalExecutions)
	}

	if n > 0 {
		sort.Float64s(durations)

		var sum float64
		for _, d := range durations {
			sum += d
		}
		snap.AvgDuration = sum / float64(n)
		snap.P50Duration = percentile(durations, n, 50)
		snap.P95Duration = percentile(durations, n, 95)
		snap.P99Duration = percentile(durations, n, 99)
	}

	return snap
}

func percentile(sorted []float64, n int, p float64) float64 {
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

// metrics.go
