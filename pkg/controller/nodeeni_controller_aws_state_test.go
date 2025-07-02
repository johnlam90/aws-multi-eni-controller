package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// MockAWSInterface is a mock implementation of aws.EC2Interface for testing
type MockAWSInterface struct {
	mock.Mock
}

func (m *MockAWSInterface) CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error) {
	args := m.Called(ctx, subnetID, securityGroupIDs, description, tags)
	return args.String(0), args.Error(1)
}

func (m *MockAWSInterface) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error) {
	args := m.Called(ctx, eniID, instanceID, deviceIndex, deleteOnTermination)
	return args.String(0), args.Error(1)
}

func (m *MockAWSInterface) DetachENI(ctx context.Context, attachmentID string, force bool) error {
	args := m.Called(ctx, attachmentID, force)
	return args.Error(0)
}

func (m *MockAWSInterface) DeleteENI(ctx context.Context, eniID string) error {
	args := m.Called(ctx, eniID)
	return args.Error(0)
}

func (m *MockAWSInterface) DescribeENI(ctx context.Context, eniID string) (*aws.EC2v2NetworkInterface, error) {
	args := m.Called(ctx, eniID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*aws.EC2v2NetworkInterface), args.Error(1)
}

func (m *MockAWSInterface) WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error {
	args := m.Called(ctx, eniID, timeout)
	return args.Error(0)
}

func (m *MockAWSInterface) GetSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	args := m.Called(ctx, subnetName)
	return args.String(0), args.Error(1)
}

func (m *MockAWSInterface) GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error) {
	args := m.Called(ctx, subnetID)
	return args.String(0), args.Error(1)
}

func (m *MockAWSInterface) GetSecurityGroupIDByName(ctx context.Context, sgName string) (string, error) {
	args := m.Called(ctx, sgName)
	return args.String(0), args.Error(1)
}

func (m *MockAWSInterface) DescribeInstance(ctx context.Context, instanceID string) (*aws.EC2Instance, error) {
	args := m.Called(ctx, instanceID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*aws.EC2Instance), args.Error(1)
}

func (m *MockAWSInterface) GetInstanceENIs(ctx context.Context, instanceID string) (map[int]string, error) {
	args := m.Called(ctx, instanceID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[int]string), args.Error(1)
}

func TestGetAWSUsedDeviceIndices(t *testing.T) {
	tests := []struct {
		name           string
		instanceID     string
		mockENIMap     map[int]string
		mockError      error
		expectedResult map[int]bool
		expectError    bool
	}{
		{
			name:       "successful query with multiple ENIs",
			instanceID: "i-1234567890abcdef0",
			mockENIMap: map[int]string{
				0: "eni-primary",
				1: "eni-vpc-cni",
				2: "eni-nodeeni-1",
			},
			expectedResult: map[int]bool{
				0: true,
				1: true,
				2: true,
			},
			expectError: false,
		},
		{
			name:           "successful query with no ENIs",
			instanceID:     "i-1234567890abcdef0",
			mockENIMap:     map[int]string{},
			expectedResult: map[int]bool{},
			expectError:    false,
		},
		{
			name:        "AWS API error",
			instanceID:  "i-1234567890abcdef0",
			mockError:   errors.New("AWS API error"),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock AWS interface
			mockAWS := &MockAWSInterface{}

			// Set up mock expectations
			if tt.mockError != nil {
				mockAWS.On("GetInstanceENIs", mock.Anything, tt.instanceID).Return(nil, tt.mockError)
			} else {
				mockAWS.On("GetInstanceENIs", mock.Anything, tt.instanceID).Return(tt.mockENIMap, nil)
			}

			// Create reconciler with mock
			reconciler := &NodeENIReconciler{
				AWS: mockAWS,
				Log: zap.New(zap.UseDevMode(true)),
			}

			// Call the method under test
			result, err := reconciler.getAWSUsedDeviceIndices(context.Background(), tt.instanceID)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}

			// Verify mock expectations
			mockAWS.AssertExpectations(t)
		})
	}
}

func TestMergeDeviceIndices(t *testing.T) {
	tests := []struct {
		name            string
		internalIndices map[int]bool
		awsIndices      map[int]bool
		expectedResult  map[int]bool
	}{
		{
			name:            "AWS and internal state match",
			internalIndices: map[int]bool{0: true, 1: true, 2: true},
			awsIndices:      map[int]bool{0: true, 1: true, 2: true},
			expectedResult:  map[int]bool{0: true, 1: true, 2: true},
		},
		{
			name:            "AWS has more ENIs than internal state",
			internalIndices: map[int]bool{0: true, 1: true},
			awsIndices:      map[int]bool{0: true, 1: true, 2: true},
			expectedResult:  map[int]bool{0: true, 1: true, 2: true},
		},
		{
			name:            "Internal state has more ENIs than AWS",
			internalIndices: map[int]bool{0: true, 1: true, 2: true},
			awsIndices:      map[int]bool{0: true, 1: true},
			expectedResult:  map[int]bool{0: true, 1: true, 2: true},
		},
		{
			name:            "Completely different indices",
			internalIndices: map[int]bool{2: true, 3: true},
			awsIndices:      map[int]bool{0: true, 1: true},
			expectedResult:  map[int]bool{0: true, 1: true, 2: true, 3: true},
		},
		{
			name:            "Empty internal state",
			internalIndices: map[int]bool{},
			awsIndices:      map[int]bool{0: true, 1: true},
			expectedResult:  map[int]bool{0: true, 1: true},
		},
		{
			name:            "Empty AWS state",
			internalIndices: map[int]bool{2: true, 3: true},
			awsIndices:      map[int]bool{},
			expectedResult:  map[int]bool{2: true, 3: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &NodeENIReconciler{
				Log: zap.New(zap.UseDevMode(true)),
			}

			result := reconciler.mergeDeviceIndices(tt.internalIndices, tt.awsIndices, logr.Discard())
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestIsDeviceIndexConflictError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "device index conflict error",
			err:      errors.New("InvalidParameterValue: Instance 'i-xxx' already has an interface attached at device index '2'"),
			expected: true,
		},
		{
			name:     "generic InvalidParameterValue with device index",
			err:      errors.New("InvalidParameterValue: Invalid device index"),
			expected: true,
		},
		{
			name:     "different AWS error",
			err:      errors.New("InvalidInstanceID.NotFound"),
			expected: false,
		},
		{
			name:     "generic error",
			err:      errors.New("some other error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &NodeENIReconciler{}
			result := reconciler.isDeviceIndexConflictError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindNextAvailableDeviceIndex(t *testing.T) {
	tests := []struct {
		name              string
		startIndex        int
		usedDeviceIndices map[int]bool
		expectedResult    int
	}{
		{
			name:              "start index is available",
			startIndex:        2,
			usedDeviceIndices: map[int]bool{0: true, 1: true},
			expectedResult:    2,
		},
		{
			name:              "start index is used, next is available",
			startIndex:        2,
			usedDeviceIndices: map[int]bool{0: true, 1: true, 2: true},
			expectedResult:    3,
		},
		{
			name:              "multiple indices are used",
			startIndex:        2,
			usedDeviceIndices: map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true},
			expectedResult:    5,
		},
		{
			name:              "empty used indices",
			startIndex:        0,
			usedDeviceIndices: map[int]bool{},
			expectedResult:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &NodeENIReconciler{}
			result := reconciler.findNextAvailableDeviceIndex(tt.startIndex, tt.usedDeviceIndices, logr.Discard())
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestAttachENIWithRetry(t *testing.T) {
	tests := []struct {
		name                    string
		eniID                   string
		instanceID              string
		deviceIndex             int
		firstAttachError        error
		awsENIMap               map[int]string
		secondAttachError       error
		expectedAttachmentID    string
		expectedDeviceIndex     int
		expectError             bool
		expectedAttachCallCount int
	}{
		{
			name:                    "successful attachment on first try",
			eniID:                   "eni-12345",
			instanceID:              "i-12345",
			deviceIndex:             2,
			firstAttachError:        nil,
			expectedAttachmentID:    "eni-attach-12345",
			expectedDeviceIndex:     2,
			expectError:             false,
			expectedAttachCallCount: 1,
		},
		{
			name:                    "device index conflict, successful retry",
			eniID:                   "eni-12345",
			instanceID:              "i-12345",
			deviceIndex:             2,
			firstAttachError:        errors.New("InvalidParameterValue: Instance 'i-12345' already has an interface attached at device index '2'"),
			awsENIMap:               map[int]string{0: "eni-primary", 1: "eni-vpc-cni", 2: "eni-existing"},
			secondAttachError:       nil,
			expectedAttachmentID:    "eni-attach-12345",
			expectedDeviceIndex:     3, // Should retry with index 3
			expectError:             false,
			expectedAttachCallCount: 2,
		},
		{
			name:                    "device index conflict, retry also fails",
			eniID:                   "eni-12345",
			instanceID:              "i-12345",
			deviceIndex:             2,
			firstAttachError:        errors.New("InvalidParameterValue: Instance 'i-12345' already has an interface attached at device index '2'"),
			awsENIMap:               map[int]string{0: "eni-primary", 1: "eni-vpc-cni", 2: "eni-existing"},
			secondAttachError:       errors.New("Another error"),
			expectedDeviceIndex:     3, // Should retry with index 3
			expectError:             true,
			expectedAttachCallCount: 2,
		},
		{
			name:                    "non-device-index error, no retry",
			eniID:                   "eni-12345",
			instanceID:              "i-12345",
			deviceIndex:             2,
			firstAttachError:        errors.New("InvalidInstanceID.NotFound"),
			expectError:             true,
			expectedAttachCallCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock AWS interface
			mockAWS := &MockAWSInterface{}

			// Set up first attach call expectation
			if tt.firstAttachError != nil {
				mockAWS.On("AttachENI", mock.Anything, tt.eniID, tt.instanceID, tt.deviceIndex, mock.AnythingOfType("bool")).Return("", tt.firstAttachError).Once()
			} else {
				mockAWS.On("AttachENI", mock.Anything, tt.eniID, tt.instanceID, tt.deviceIndex, mock.AnythingOfType("bool")).Return(tt.expectedAttachmentID, nil).Once()
			}

			// Set up retry logic expectations if needed
			if tt.firstAttachError != nil {
				reconciler := &NodeENIReconciler{}
				if reconciler.isDeviceIndexConflictError(tt.firstAttachError) {
					// Expect GetInstanceENIs call for retry logic
					mockAWS.On("GetInstanceENIs", mock.Anything, tt.instanceID).Return(tt.awsENIMap, nil).Once()

					// Expect second attach call if retry is attempted
					if tt.expectedAttachCallCount > 1 {
						if tt.secondAttachError != nil {
							mockAWS.On("AttachENI", mock.Anything, tt.eniID, tt.instanceID, tt.expectedDeviceIndex, mock.AnythingOfType("bool")).Return("", tt.secondAttachError).Once()
						} else {
							mockAWS.On("AttachENI", mock.Anything, tt.eniID, tt.instanceID, tt.expectedDeviceIndex, mock.AnythingOfType("bool")).Return(tt.expectedAttachmentID, nil).Once()
						}
					}
				}
			}

			// Create reconciler with mock
			reconciler := &NodeENIReconciler{
				AWS: mockAWS,
				Log: zap.New(zap.UseDevMode(true)),
				Config: &config.ControllerConfig{
					DefaultDeleteOnTermination: false,
				},
			}

			// Call the method under test
			attachmentID, actualDeviceIndex, err := reconciler.attachENI(context.Background(), tt.eniID, tt.instanceID, tt.deviceIndex)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedAttachmentID, attachmentID)
				assert.Equal(t, tt.expectedDeviceIndex, actualDeviceIndex)
			}

			// Verify mock expectations
			mockAWS.AssertExpectations(t)
		})
	}
}
