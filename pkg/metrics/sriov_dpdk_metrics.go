package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// DPDK operation metrics
	dpdkBindOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aws_multi_eni_dpdk_bind_operations_total",
			Help: "Total number of DPDK bind operations",
		},
		[]string{"status", "driver", "operation_type"},
	)

	dpdkBindOperationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aws_multi_eni_dpdk_bind_operation_duration_seconds",
			Help:    "Duration of DPDK bind operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status", "driver", "operation_type"},
	)

	// SR-IOV configuration metrics
	sriovConfigOperationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aws_multi_eni_sriov_config_operations_total",
			Help: "Total number of SR-IOV configuration operations",
		},
		[]string{"status", "operation_type"},
	)

	sriovConfigOperationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "aws_multi_eni_sriov_config_operation_duration_seconds",
			Help:    "Duration of SR-IOV configuration operations",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"status", "operation_type"},
	)

	// Current state metrics
	dpdkBoundInterfacesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aws_multi_eni_dpdk_bound_interfaces",
			Help: "Number of interfaces currently bound to DPDK drivers",
		},
		[]string{"driver", "node"},
	)

	sriovResourcesGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "aws_multi_eni_sriov_resources",
			Help: "Number of SR-IOV resources configured",
		},
		[]string{"resource_name", "node"},
	)

	// Error metrics
	dpdkOperationErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aws_multi_eni_dpdk_operation_errors_total",
			Help: "Total number of DPDK operation errors",
		},
		[]string{"error_type", "operation"},
	)

	sriovConfigErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "aws_multi_eni_sriov_config_errors_total",
			Help: "Total number of SR-IOV configuration errors",
		},
		[]string{"error_type", "operation"},
	)

	// Concurrent operation metrics
	concurrentDpdkOperationsGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "aws_multi_eni_concurrent_dpdk_operations",
			Help: "Number of concurrent DPDK operations in progress",
		},
	)

	// Memory usage metrics
	dpdkOperationsMapSizeGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "aws_multi_eni_dpdk_operations_map_size",
			Help: "Size of the DPDK operations tracking map",
		},
	)
)

// MetricsCollector provides methods for collecting SR-IOV and DPDK metrics
type MetricsCollector struct {
	mu sync.RWMutex
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{}
}

// RecordDPDKBindOperation records a DPDK bind operation
func (m *MetricsCollector) RecordDPDKBindOperation(driver, operationType string, duration time.Duration, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}

	dpdkBindOperationsTotal.WithLabelValues(status, driver, operationType).Inc()
	dpdkBindOperationDuration.WithLabelValues(status, driver, operationType).Observe(duration.Seconds())
}

// RecordSRIOVConfigOperation records an SR-IOV configuration operation
func (m *MetricsCollector) RecordSRIOVConfigOperation(operationType string, duration time.Duration, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}

	sriovConfigOperationsTotal.WithLabelValues(status, operationType).Inc()
	sriovConfigOperationDuration.WithLabelValues(status, operationType).Observe(duration.Seconds())
}

// UpdateDPDKBoundInterfacesCount updates the count of DPDK bound interfaces
func (m *MetricsCollector) UpdateDPDKBoundInterfacesCount(driver, node string, count float64) {
	dpdkBoundInterfacesGauge.WithLabelValues(driver, node).Set(count)
}

// UpdateSRIOVResourcesCount updates the count of SR-IOV resources
func (m *MetricsCollector) UpdateSRIOVResourcesCount(resourceName, node string, count float64) {
	sriovResourcesGauge.WithLabelValues(resourceName, node).Set(count)
}

// RecordDPDKOperationError records a DPDK operation error
func (m *MetricsCollector) RecordDPDKOperationError(errorType, operation string) {
	dpdkOperationErrorsTotal.WithLabelValues(errorType, operation).Inc()
}

// RecordSRIOVConfigError records an SR-IOV configuration error
func (m *MetricsCollector) RecordSRIOVConfigError(errorType, operation string) {
	sriovConfigErrorsTotal.WithLabelValues(errorType, operation).Inc()
}

// UpdateConcurrentDPDKOperations updates the count of concurrent DPDK operations
func (m *MetricsCollector) UpdateConcurrentDPDKOperations(count float64) {
	concurrentDpdkOperationsGauge.Set(count)
}

// UpdateDPDKOperationsMapSize updates the size of the DPDK operations map
func (m *MetricsCollector) UpdateDPDKOperationsMapSize(size float64) {
	dpdkOperationsMapSizeGauge.Set(size)
}

// DPDKOperationTimer provides timing functionality for DPDK operations
type DPDKOperationTimer struct {
	startTime     time.Time
	driver        string
	operationType string
	collector     *MetricsCollector
}

// NewDPDKOperationTimer creates a new DPDK operation timer
func (m *MetricsCollector) NewDPDKOperationTimer(driver, operationType string) *DPDKOperationTimer {
	return &DPDKOperationTimer{
		startTime:     time.Now(),
		driver:        driver,
		operationType: operationType,
		collector:     m,
	}
}

// Finish records the completion of a DPDK operation
func (t *DPDKOperationTimer) Finish(success bool) {
	duration := time.Since(t.startTime)
	t.collector.RecordDPDKBindOperation(t.driver, t.operationType, duration, success)
}

// SRIOVOperationTimer provides timing functionality for SR-IOV operations
type SRIOVOperationTimer struct {
	startTime     time.Time
	operationType string
	collector     *MetricsCollector
}

// NewSRIOVOperationTimer creates a new SR-IOV operation timer
func (m *MetricsCollector) NewSRIOVOperationTimer(operationType string) *SRIOVOperationTimer {
	return &SRIOVOperationTimer{
		startTime:     time.Now(),
		operationType: operationType,
		collector:     m,
	}
}

// Finish records the completion of an SR-IOV operation
func (t *SRIOVOperationTimer) Finish(success bool) {
	duration := time.Since(t.startTime)
	t.collector.RecordSRIOVConfigOperation(t.operationType, duration, success)
}

// ErrorType constants for consistent error categorization
const (
	ErrorTypePCINotFound        = "pci_not_found"
	ErrorTypeDriverNotFound     = "driver_not_found"
	ErrorTypePermissionDenied   = "permission_denied"
	ErrorTypeDeviceInUse        = "device_in_use"
	ErrorTypeConfigFileError    = "config_file_error"
	ErrorTypeValidationError    = "validation_error"
	ErrorTypeConcurrencyError   = "concurrency_error"
	ErrorTypeTimeoutError       = "timeout_error"
	ErrorTypeUnknownError       = "unknown_error"
)

// OperationType constants for consistent operation categorization
const (
	OperationTypeBind           = "bind"
	OperationTypeUnbind         = "unbind"
	OperationTypeConfigUpdate   = "config_update"
	OperationTypeConfigValidate = "config_validate"
	OperationTypeConfigBackup   = "config_backup"
	OperationTypeConfigRestore  = "config_restore"
)

// Global metrics collector instance
var GlobalMetricsCollector = NewMetricsCollector()

// Helper functions for common metric operations

// RecordDPDKBindSuccess records a successful DPDK bind operation
func RecordDPDKBindSuccess(driver string, duration time.Duration) {
	GlobalMetricsCollector.RecordDPDKBindOperation(driver, OperationTypeBind, duration, true)
}

// RecordDPDKBindFailure records a failed DPDK bind operation
func RecordDPDKBindFailure(driver string, duration time.Duration, errorType string) {
	GlobalMetricsCollector.RecordDPDKBindOperation(driver, OperationTypeBind, duration, false)
	GlobalMetricsCollector.RecordDPDKOperationError(errorType, OperationTypeBind)
}

// RecordSRIOVConfigSuccess records a successful SR-IOV configuration operation
func RecordSRIOVConfigSuccess(operationType string, duration time.Duration) {
	GlobalMetricsCollector.RecordSRIOVConfigOperation(operationType, duration, true)
}

// RecordSRIOVConfigFailure records a failed SR-IOV configuration operation
func RecordSRIOVConfigFailure(operationType string, duration time.Duration, errorType string) {
	GlobalMetricsCollector.RecordSRIOVConfigOperation(operationType, duration, false)
	GlobalMetricsCollector.RecordSRIOVConfigError(errorType, operationType)
}
