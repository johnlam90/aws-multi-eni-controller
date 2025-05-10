package aws

// EC2v2NetworkInterfaceStatus represents the status of a network interface in AWS SDK v2
type EC2v2NetworkInterfaceStatus string

const (
	// EC2v2NetworkInterfaceStatusAvailable represents the "available" status
	EC2v2NetworkInterfaceStatusAvailable EC2v2NetworkInterfaceStatus = "available"
	// EC2v2NetworkInterfaceStatusInUse represents the "in-use" status
	EC2v2NetworkInterfaceStatusInUse EC2v2NetworkInterfaceStatus = "in-use"
)

// EC2v2NetworkInterfaceAttachment represents a network interface attachment in AWS SDK v2
type EC2v2NetworkInterfaceAttachment struct {
	AttachmentID        string
	DeleteOnTermination bool
	DeviceIndex         int32
	InstanceID          string
	Status              string
}

// EC2v2NetworkInterface represents a network interface in AWS SDK v2
type EC2v2NetworkInterface struct {
	NetworkInterfaceID string
	Status             EC2v2NetworkInterfaceStatus
	SubnetID           string
	SubnetCIDR         string
	Attachment         *EC2v2NetworkInterfaceAttachment
}
