package coordinator

import (
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

func TestParseResourceName(t *testing.T) {
	// Create a minimal manager for testing
	cfg := &config.ENIManagerConfig{}
	manager := &Manager{config: cfg}

	tests := []struct {
		name           string
		input          string
		expectedPrefix string
		expectedName   string
	}{
		{
			name:           "Full resource name with prefix",
			input:          "intel.com/sriov_kernel_1",
			expectedPrefix: "intel.com",
			expectedName:   "sriov_kernel_1",
		},
		{
			name:           "Full resource name with prefix 2",
			input:          "intel.com/sriov_kernel_2",
			expectedPrefix: "intel.com",
			expectedName:   "sriov_kernel_2",
		},
		{
			name:           "Resource name without prefix",
			input:          "sriov_test",
			expectedPrefix: "intel.com",
			expectedName:   "sriov_test",
		},
		{
			name:           "Empty resource name",
			input:          "",
			expectedPrefix: "intel.com",
			expectedName:   "sriov_net",
		},
		{
			name:           "Amazon prefix",
			input:          "amazon.com/ena_kernel_test",
			expectedPrefix: "amazon.com",
			expectedName:   "ena_kernel_test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, name := manager.parseResourceName(tt.input)
			
			if prefix != tt.expectedPrefix {
				t.Errorf("parseResourceName(%q) prefix = %q, want %q", tt.input, prefix, tt.expectedPrefix)
			}
			
			if name != tt.expectedName {
				t.Errorf("parseResourceName(%q) name = %q, want %q", tt.input, name, tt.expectedName)
			}
		})
	}
}
