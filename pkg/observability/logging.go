package observability

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
)

// StructuredLogger provides structured logging with consistent fields
type StructuredLogger struct {
	logger  logr.Logger
	metrics *Metrics
}

// NewStructuredLogger creates a new structured logger
func NewStructuredLogger(logger logr.Logger, metrics *Metrics) *StructuredLogger {
	return &StructuredLogger{
		logger:  logger,
		metrics: metrics,
	}
}

// OperationContext holds context information for an operation
type OperationContext struct {
	OperationID   string
	OperationType string
	NodeENI       string
	NodeID        string
	ENIID         string
	InstanceID    string
	StartTime     time.Time
	Metadata      map[string]interface{}
}

// NewOperationContext creates a new operation context
func NewOperationContext(operationType string) *OperationContext {
	return &OperationContext{
		OperationID:   generateOperationID(),
		OperationType: operationType,
		StartTime:     time.Now(),
		Metadata:      make(map[string]interface{}),
	}
}

// WithNodeENI adds NodeENI information to the context
func (oc *OperationContext) WithNodeENI(nodeENI *networkingv1alpha1.NodeENI) *OperationContext {
	oc.NodeENI = nodeENI.Name
	return oc
}

// WithAttachment adds attachment information to the context
func (oc *OperationContext) WithAttachment(attachment networkingv1alpha1.ENIAttachment) *OperationContext {
	oc.NodeID = attachment.NodeID
	oc.ENIID = attachment.ENIID
	oc.InstanceID = attachment.InstanceID
	return oc
}

// WithMetadata adds metadata to the context
func (oc *OperationContext) WithMetadata(key string, value interface{}) *OperationContext {
	oc.Metadata[key] = value
	return oc
}

// Duration returns the elapsed time since the operation started
func (oc *OperationContext) Duration() time.Duration {
	return time.Since(oc.StartTime)
}

// LogFields returns structured log fields for this context
func (oc *OperationContext) LogFields() []interface{} {
	fields := []interface{}{
		"operationID", oc.OperationID,
		"operationType", oc.OperationType,
		"duration", oc.Duration(),
	}

	if oc.NodeENI != "" {
		fields = append(fields, "nodeENI", oc.NodeENI)
	}
	if oc.NodeID != "" {
		fields = append(fields, "nodeID", oc.NodeID)
	}
	if oc.ENIID != "" {
		fields = append(fields, "eniID", oc.ENIID)
	}
	if oc.InstanceID != "" {
		fields = append(fields, "instanceID", oc.InstanceID)
	}

	// Add metadata fields
	for key, value := range oc.Metadata {
		fields = append(fields, key, value)
	}

	return fields
}

// LogOperationStart logs the start of an operation
func (sl *StructuredLogger) LogOperationStart(ctx context.Context, opCtx *OperationContext, message string) {
	fields := opCtx.LogFields()
	sl.logger.Info(message, fields...)
}

// LogOperationSuccess logs successful completion of an operation
func (sl *StructuredLogger) LogOperationSuccess(ctx context.Context, opCtx *OperationContext, message string) {
	fields := opCtx.LogFields()
	sl.logger.Info(message, fields...)

	// Record metrics
	if sl.metrics != nil {
		switch opCtx.OperationType {
		case "eni-attach", "eni-detach", "eni-create", "eni-delete":
			sl.metrics.RecordENIOperation(opCtx.OperationType, "success", opCtx.NodeID, opCtx.Duration())
		case "cleanup":
			sl.metrics.RecordCleanupOperation(opCtx.OperationType, "success", opCtx.Duration())
		case "dpdk-bind", "dpdk-unbind":
			sl.metrics.RecordDPDKOperation(opCtx.OperationType, "success", opCtx.NodeID)
		}
	}
}

// LogOperationError logs an error during an operation
func (sl *StructuredLogger) LogOperationError(ctx context.Context, opCtx *OperationContext, err error, message string) {
	fields := append(opCtx.LogFields(), "error", err.Error())
	sl.logger.Error(err, message, fields...)

	// Record metrics
	if sl.metrics != nil {
		errorType := categorizeError(err)
		switch opCtx.OperationType {
		case "eni-attach", "eni-detach", "eni-create", "eni-delete":
			sl.metrics.RecordENIError(opCtx.OperationType, errorType, opCtx.NodeID)
		case "cleanup":
			sl.metrics.RecordCleanupError(opCtx.OperationType, errorType)
		case "dpdk-bind", "dpdk-unbind":
			sl.metrics.RecordDPDKBindingError(errorType, opCtx.NodeID)
		}
	}
}

// LogOperationWarning logs a warning during an operation
func (sl *StructuredLogger) LogOperationWarning(ctx context.Context, opCtx *OperationContext, message string) {
	fields := opCtx.LogFields()
	sl.logger.Info(fmt.Sprintf("WARNING: %s", message), fields...)
}

// LogAWSAPICall logs an AWS API call with timing
func (sl *StructuredLogger) LogAWSAPICall(ctx context.Context, service, operation string, duration time.Duration, err error) {
	fields := []interface{}{
		"service", service,
		"operation", operation,
		"duration", duration,
	}

	if err != nil {
		fields = append(fields, "error", err.Error())
		sl.logger.Error(err, "AWS API call failed", fields...)

		if sl.metrics != nil {
			errorCategory := categorizeError(err)
			sl.metrics.RecordAWSAPIError(service, operation, errorCategory)
			sl.metrics.RecordAWSAPICall(service, operation, "error", duration)
		}
	} else {
		sl.logger.Info("AWS API call succeeded", fields...)

		if sl.metrics != nil {
			sl.metrics.RecordAWSAPICall(service, operation, "success", duration)
		}
	}
}

// LogCircuitBreakerEvent logs circuit breaker state changes
func (sl *StructuredLogger) LogCircuitBreakerEvent(ctx context.Context, operationType string, oldState, newState int, reason string) {
	fields := []interface{}{
		"operationType", operationType,
		"oldState", stateToString(oldState),
		"newState", stateToString(newState),
		"reason", reason,
	}

	sl.logger.Info("Circuit breaker state changed", fields...)

	if sl.metrics != nil {
		sl.metrics.RecordCircuitBreakerState(operationType, newState)
	}
}

// LogCoordinationEvent logs coordination-related events
func (sl *StructuredLogger) LogCoordinationEvent(ctx context.Context, eventType string, resourceIDs []string, operationID string, details map[string]interface{}) {
	fields := []interface{}{
		"eventType", eventType,
		"operationID", operationID,
		"resourceIDs", resourceIDs,
	}

	for key, value := range details {
		fields = append(fields, key, value)
	}

	sl.logger.Info("Coordination event", fields...)

	if sl.metrics != nil && eventType == "conflict" {
		sl.metrics.RecordCoordinationConflict()
	}
}

// LogStaleResourceDetection logs stale resource detection events
func (sl *StructuredLogger) LogStaleResourceDetection(ctx context.Context, resourceType, resourceID string, reason string, action string) {
	fields := []interface{}{
		"resourceType", resourceType,
		"resourceID", resourceID,
		"reason", reason,
		"action", action,
	}

	sl.logger.Info("Stale resource detected", fields...)

	if sl.metrics != nil && action == "cleanup" {
		sl.metrics.RecordStaleCleanup()
	}
}

// LogDPDKRollback logs DPDK rollback events
func (sl *StructuredLogger) LogDPDKRollback(ctx context.Context, opCtx *OperationContext, reason string, rollbackSuccess bool) {
	fields := append(opCtx.LogFields(),
		"reason", reason,
		"rollbackSuccess", rollbackSuccess,
	)

	if rollbackSuccess {
		sl.logger.Info("DPDK operation rolled back successfully", fields...)
	} else {
		sl.logger.Error(nil, "DPDK rollback failed", fields...)
	}

	if sl.metrics != nil {
		sl.metrics.RecordDPDKRollback()
	}
}

// LogResourceMetrics logs current resource metrics
func (sl *StructuredLogger) LogResourceMetrics(ctx context.Context, attachmentCount, detachmentCount, lockCount int) {
	fields := []interface{}{
		"attachmentCount", attachmentCount,
		"detachmentCount", detachmentCount,
		"lockCount", lockCount,
	}

	sl.logger.Info("Resource metrics", fields...)

	if sl.metrics != nil {
		sl.metrics.UpdateENIAttachments(attachmentCount)
		sl.metrics.UpdateENIDetachments(detachmentCount)
		sl.metrics.UpdateResourceLocks(lockCount)
	}
}

// Helper functions

// generateOperationID generates a unique operation ID
func generateOperationID() string {
	return fmt.Sprintf("op-%d", time.Now().UnixNano())
}

// categorizeError categorizes errors for metrics
func categorizeError(err error) string {
	if err == nil {
		return "none"
	}

	errMsg := err.Error()

	// AWS-specific errors
	if contains(errMsg, "throttling", "rate limit", "requestlimitexceeded") {
		return "throttling"
	}
	if contains(errMsg, "access denied", "unauthorized", "forbidden") {
		return "authorization"
	}
	if contains(errMsg, "not found", "does not exist") {
		return "not_found"
	}
	if contains(errMsg, "timeout", "connection") {
		return "network"
	}
	if contains(errMsg, "invalid", "malformed", "bad request") {
		return "validation"
	}

	return "unknown"
}

// stateToString converts circuit breaker state to string
func stateToString(state int) string {
	switch state {
	case 0:
		return "closed"
	case 1:
		return "open"
	case 2:
		return "half-open"
	default:
		return "unknown"
	}
}

// contains checks if any of the substrings are contained in the main string
func contains(str string, substrings ...string) bool {
	for _, substr := range substrings {
		if len(str) >= len(substr) {
			for i := 0; i <= len(str)-len(substr); i++ {
				if str[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}
