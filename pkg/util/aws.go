// Package util provides utility functions used throughout the AWS Multi-ENI Controller.
//
// This package contains helper functions for common operations such as string manipulation,
// AWS resource identification, and other utility functions that don't fit elsewhere.
package util

import (
	"strings"
)

// GetInstanceIDFromProviderID extracts the EC2 instance ID from the provider ID
// Provider ID format: aws:///zone/i-0123456789abcdef0
func GetInstanceIDFromProviderID(providerID string) string {
	parts := strings.Split(providerID, "/")
	if len(parts) < 2 {
		return ""
	}
	// The instance ID should be the last part
	return parts[len(parts)-1]
}

// ContainsString checks if a string is in a slice of strings
func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// RemoveString removes a string from a slice of strings
func RemoveString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

// MergeMaps merges two maps, with values from the second map taking precedence
func MergeMaps(m1, m2 map[string]string) map[string]string {
	result := make(map[string]string)

	// Copy all keys from m1
	for k, v := range m1 {
		result[k] = v
	}

	// Copy all keys from m2, overwriting any from m1
	for k, v := range m2 {
		result[k] = v
	}

	return result
}
