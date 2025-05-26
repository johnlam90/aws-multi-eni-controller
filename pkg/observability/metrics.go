package observability

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all the Prometheus metrics for the controller
type Metrics struct {
	// ENI operation metrics
	ENIOperationsTotal   *prometheus.CounterVec
	ENIOperationDuration *prometheus.HistogramVec
	ENIOperationErrors   *prometheus.CounterVec
	ENIAttachmentsTotal  prometheus.Gauge
	ENIDetachmentsTotal  prometheus.Gauge

	// Cleanup operation metrics
	CleanupOperationsTotal *prometheus.CounterVec
	CleanupDuration        *prometheus.HistogramVec
	CleanupErrors          *prometheus.CounterVec
	StaleCleanupsTotal     prometheus.Counter

	// Circuit breaker metrics
	CircuitBreakerState      *prometheus.GaugeVec
	CircuitBreakerOperations *prometheus.CounterVec

	// Coordination metrics
	ResourceLocks         prometheus.Gauge
	CoordinationConflicts prometheus.Counter
	CoordinationWaitTime  prometheus.Histogram

	// DPDK operation metrics
	DPDKOperationsTotal *prometheus.CounterVec
	DPDKRollbacksTotal  prometheus.Counter
	DPDKBindingErrors   *prometheus.CounterVec

	// AWS API metrics
	AWSAPICallsTotal    *prometheus.CounterVec
	AWSAPICallDuration  *prometheus.HistogramVec
	AWSAPIErrors        *prometheus.CounterVec
	AWSThrottlingEvents prometheus.Counter
}

var (
	metricsInstance *Metrics
	metricsOnce     sync.Once
)

// NewMetrics creates and registers all metrics (singleton pattern)
func NewMetrics() *Metrics {
	metricsOnce.Do(func() {
		metricsInstance = createMetrics()
	})
	return metricsInstance
}

// createMetrics creates and registers all metrics
func createMetrics() *Metrics {
	m := &Metrics{
		// ENI operation metrics
		ENIOperationsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "eni_operations_total",
				Help: "Total number of ENI operations performed",
			},
			[]string{"operation", "status", "node"},
		),

		ENIOperationDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "eni_operation_duration_seconds",
				Help:    "Duration of ENI operations in seconds",
				Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
			},
			[]string{"operation", "node"},
		),

		ENIOperationErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "eni_operation_errors_total",
				Help: "Total number of ENI operation errors",
			},
			[]string{"operation", "error_type", "node"},
		),

		ENIAttachmentsTotal: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "eni_attachments_total",
				Help: "Current number of ENI attachments",
			},
		),

		ENIDetachmentsTotal: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "eni_detachments_total",
				Help: "Current number of ENI detachments in progress",
			},
		),

		// Cleanup operation metrics
		CleanupOperationsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cleanup_operations_total",
				Help: "Total number of cleanup operations performed",
			},
			[]string{"type", "status"},
		),

		CleanupDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "cleanup_duration_seconds",
				Help:    "Duration of cleanup operations in seconds",
				Buckets: prometheus.ExponentialBuckets(1, 2, 10),
			},
			[]string{"type"},
		),

		CleanupErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cleanup_errors_total",
				Help: "Total number of cleanup errors",
			},
			[]string{"type", "error_category"},
		),

		StaleCleanupsTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "stale_cleanups_total",
				Help: "Total number of stale resource cleanups performed",
			},
		),

		// Circuit breaker metrics
		CircuitBreakerState: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "circuit_breaker_state",
				Help: "Current state of circuit breakers (0=closed, 1=open, 2=half-open)",
			},
			[]string{"operation_type"},
		),

		CircuitBreakerOperations: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "circuit_breaker_operations_total",
				Help: "Total number of operations processed by circuit breaker",
			},
			[]string{"operation_type", "result"},
		),

		// Coordination metrics
		ResourceLocks: promauto.NewGauge(
			prometheus.GaugeOpts{
				Name: "resource_locks_active",
				Help: "Current number of active resource locks",
			},
		),

		CoordinationConflicts: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "coordination_conflicts_total",
				Help: "Total number of resource coordination conflicts",
			},
		),

		CoordinationWaitTime: promauto.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "coordination_wait_time_seconds",
				Help:    "Time spent waiting for resource coordination",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
			},
		),

		// DPDK operation metrics
		DPDKOperationsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dpdk_operations_total",
				Help: "Total number of DPDK operations performed",
			},
			[]string{"operation", "status", "node"},
		),

		DPDKRollbacksTotal: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "dpdk_rollbacks_total",
				Help: "Total number of DPDK operation rollbacks performed",
			},
		),

		DPDKBindingErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dpdk_binding_errors_total",
				Help: "Total number of DPDK binding errors",
			},
			[]string{"error_type", "node"},
		),

		// AWS API metrics
		AWSAPICallsTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "aws_api_calls_total",
				Help: "Total number of AWS API calls made",
			},
			[]string{"service", "operation", "status"},
		),

		AWSAPICallDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "aws_api_call_duration_seconds",
				Help:    "Duration of AWS API calls in seconds",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
			},
			[]string{"service", "operation"},
		),

		AWSAPIErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "aws_api_errors_total",
				Help: "Total number of AWS API errors",
			},
			[]string{"service", "operation", "error_category"},
		),

		AWSThrottlingEvents: promauto.NewCounter(
			prometheus.CounterOpts{
				Name: "aws_throttling_events_total",
				Help: "Total number of AWS API throttling events",
			},
		),
	}

	return m
}

// RecordENIOperation records metrics for an ENI operation
func (m *Metrics) RecordENIOperation(operation, status, node string, duration time.Duration) {
	m.ENIOperationsTotal.WithLabelValues(operation, status, node).Inc()
	m.ENIOperationDuration.WithLabelValues(operation, node).Observe(duration.Seconds())
}

// RecordENIError records an ENI operation error
func (m *Metrics) RecordENIError(operation, errorType, node string) {
	m.ENIOperationErrors.WithLabelValues(operation, errorType, node).Inc()
}

// RecordCleanupOperation records metrics for a cleanup operation
func (m *Metrics) RecordCleanupOperation(cleanupType, status string, duration time.Duration) {
	m.CleanupOperationsTotal.WithLabelValues(cleanupType, status).Inc()
	m.CleanupDuration.WithLabelValues(cleanupType).Observe(duration.Seconds())
}

// RecordCleanupError records a cleanup error
func (m *Metrics) RecordCleanupError(cleanupType, errorCategory string) {
	m.CleanupErrors.WithLabelValues(cleanupType, errorCategory).Inc()
}

// RecordCircuitBreakerState records the current state of a circuit breaker
func (m *Metrics) RecordCircuitBreakerState(operationType string, state int) {
	m.CircuitBreakerState.WithLabelValues(operationType).Set(float64(state))
}

// RecordCircuitBreakerOperation records a circuit breaker operation
func (m *Metrics) RecordCircuitBreakerOperation(operationType, result string) {
	m.CircuitBreakerOperations.WithLabelValues(operationType, result).Inc()
}

// RecordDPDKOperation records metrics for a DPDK operation
func (m *Metrics) RecordDPDKOperation(operation, status, node string) {
	m.DPDKOperationsTotal.WithLabelValues(operation, status, node).Inc()
}

// RecordDPDKRollback records a DPDK rollback
func (m *Metrics) RecordDPDKRollback() {
	m.DPDKRollbacksTotal.Inc()
}

// RecordDPDKBindingError records a DPDK binding error
func (m *Metrics) RecordDPDKBindingError(errorType, node string) {
	m.DPDKBindingErrors.WithLabelValues(errorType, node).Inc()
}

// RecordAWSAPICall records metrics for an AWS API call
func (m *Metrics) RecordAWSAPICall(service, operation, status string, duration time.Duration) {
	m.AWSAPICallsTotal.WithLabelValues(service, operation, status).Inc()
	m.AWSAPICallDuration.WithLabelValues(service, operation).Observe(duration.Seconds())
}

// RecordAWSAPIError records an AWS API error
func (m *Metrics) RecordAWSAPIError(service, operation, errorCategory string) {
	m.AWSAPIErrors.WithLabelValues(service, operation, errorCategory).Inc()
}

// RecordAWSThrottling records an AWS throttling event
func (m *Metrics) RecordAWSThrottling() {
	m.AWSThrottlingEvents.Inc()
}

// UpdateResourceLocks updates the current number of resource locks
func (m *Metrics) UpdateResourceLocks(count int) {
	m.ResourceLocks.Set(float64(count))
}

// RecordCoordinationConflict records a coordination conflict
func (m *Metrics) RecordCoordinationConflict() {
	m.CoordinationConflicts.Inc()
}

// RecordCoordinationWaitTime records time spent waiting for coordination
func (m *Metrics) RecordCoordinationWaitTime(duration time.Duration) {
	m.CoordinationWaitTime.Observe(duration.Seconds())
}

// UpdateENIAttachments updates the current number of ENI attachments
func (m *Metrics) UpdateENIAttachments(count int) {
	m.ENIAttachmentsTotal.Set(float64(count))
}

// UpdateENIDetachments updates the current number of ENI detachments
func (m *Metrics) UpdateENIDetachments(count int) {
	m.ENIDetachmentsTotal.Set(float64(count))
}

// RecordStaleCleanup records a stale resource cleanup
func (m *Metrics) RecordStaleCleanup() {
	m.StaleCleanupsTotal.Inc()
}

// Timer is a helper for timing operations
type Timer struct {
	start time.Time
}

// NewTimer creates a new timer
func NewTimer() *Timer {
	return &Timer{start: time.Now()}
}

// Duration returns the elapsed time since the timer was created
func (t *Timer) Duration() time.Duration {
	return time.Since(t.start)
}

// ObserveWithTimer is a helper to observe a histogram with a timer
func ObserveWithTimer(histogram prometheus.Observer, timer *Timer) {
	histogram.Observe(timer.Duration().Seconds())
}
