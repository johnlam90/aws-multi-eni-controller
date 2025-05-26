package network

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// ValidateInterfaceName validates a network interface name
func ValidateInterfaceName(name string) error {
	if name == "" {
		return fmt.Errorf("interface name cannot be empty")
	}

	// Check length (Linux interface names are limited to 15 characters)
	if len(name) > 15 {
		return fmt.Errorf("interface name '%s' is too long (max 15 characters)", name)
	}

	// Check for valid characters (alphanumeric, dash, underscore)
	validName := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !validName.MatchString(name) {
		return fmt.Errorf("interface name '%s' contains invalid characters", name)
	}

	return nil
}

// ValidateMTU validates an MTU value
func ValidateMTU(mtu int) error {
	// Standard Ethernet MTU range
	const (
		MinMTU = 68   // Minimum IPv4 MTU
		MaxMTU = 9000 // Jumbo frame MTU
	)

	if mtu < MinMTU {
		return fmt.Errorf("MTU %d is too small (minimum: %d)", mtu, MinMTU)
	}

	if mtu > MaxMTU {
		return fmt.Errorf("MTU %d is too large (maximum: %d)", mtu, MaxMTU)
	}

	return nil
}

// ValidatePCIAddress validates a PCI address format
func ValidatePCIAddress(addr string) error {
	if addr == "" {
		return fmt.Errorf("PCI address cannot be empty")
	}

	// PCI address format: DDDD:BB:DD.F (domain:bus:device.function)
	// Example: 0000:00:06.0
	pciRegex := regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)
	if !pciRegex.MatchString(addr) {
		return fmt.Errorf("invalid PCI address format '%s' (expected: DDDD:BB:DD.F)", addr)
	}

	return nil
}

// ValidateMACAddress validates a MAC address format
func ValidateMACAddress(mac string) error {
	if mac == "" {
		return fmt.Errorf("MAC address cannot be empty")
	}

	_, err := net.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("invalid MAC address format '%s': %v", mac, err)
	}

	return nil
}

// ValidateDeviceIndex validates a device index
func ValidateDeviceIndex(index int) error {
	// AWS ENI device indices typically range from 0 to 15
	const (
		MinDeviceIndex = 0
		MaxDeviceIndex = 15
	)

	if index < MinDeviceIndex {
		return fmt.Errorf("device index %d is too small (minimum: %d)", index, MinDeviceIndex)
	}

	if index > MaxDeviceIndex {
		return fmt.Errorf("device index %d is too large (maximum: %d)", index, MaxDeviceIndex)
	}

	return nil
}

// ValidateENIPattern validates an ENI pattern regex
func ValidateENIPattern(pattern string) error {
	if pattern == "" {
		return fmt.Errorf("ENI pattern cannot be empty")
	}

	_, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid ENI pattern '%s': %v", pattern, err)
	}

	return nil
}

// ValidateInterfaceState validates an interface state string
func ValidateInterfaceState(state string) error {
	validStates := []string{"UP", "DOWN", "UNKNOWN"}
	
	state = strings.ToUpper(state)
	for _, validState := range validStates {
		if state == validState {
			return nil
		}
	}

	return fmt.Errorf("invalid interface state '%s' (valid: %s)", state, strings.Join(validStates, ", "))
}

// ParseInterfaceIndex extracts the numeric index from an interface name
func ParseInterfaceIndex(ifaceName string) (int, error) {
	// Extract numeric suffix from interface names like eth1, ens5, etc.
	re := regexp.MustCompile(`(\d+)$`)
	matches := re.FindStringSubmatch(ifaceName)
	
	if len(matches) < 2 {
		return 0, fmt.Errorf("no numeric index found in interface name '%s'", ifaceName)
	}

	index, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("failed to parse numeric index from '%s': %v", ifaceName, err)
	}

	return index, nil
}

// IsValidInterfaceForENI checks if an interface name is valid for ENI operations
func IsValidInterfaceForENI(ifaceName string, eniPattern string, ignoreList []string) (bool, error) {
	// Validate interface name format
	if err := ValidateInterfaceName(ifaceName); err != nil {
		return false, err
	}

	// Check against ignore list
	for _, ignored := range ignoreList {
		if ifaceName == ignored {
			return false, nil
		}
	}

	// Check against ENI pattern
	if eniPattern != "" {
		matched, err := regexp.MatchString(eniPattern, ifaceName)
		if err != nil {
			return false, fmt.Errorf("error matching ENI pattern: %v", err)
		}
		return matched, nil
	}

	return true, nil
}

// ValidateNetworkConfiguration validates a complete network configuration
func ValidateNetworkConfiguration(ifaceName string, mtu int, deviceIndex int, pciAddress string) error {
	if err := ValidateInterfaceName(ifaceName); err != nil {
		return fmt.Errorf("interface name validation failed: %v", err)
	}

	if mtu > 0 {
		if err := ValidateMTU(mtu); err != nil {
			return fmt.Errorf("MTU validation failed: %v", err)
		}
	}

	if err := ValidateDeviceIndex(deviceIndex); err != nil {
		return fmt.Errorf("device index validation failed: %v", err)
	}

	if pciAddress != "" {
		if err := ValidatePCIAddress(pciAddress); err != nil {
			return fmt.Errorf("PCI address validation failed: %v", err)
		}
	}

	return nil
}

// SanitizeInterfaceName sanitizes an interface name by removing invalid characters
func SanitizeInterfaceName(name string) string {
	// Replace invalid characters with underscores
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	sanitized := re.ReplaceAllString(name, "_")

	// Truncate to maximum length
	if len(sanitized) > 15 {
		sanitized = sanitized[:15]
	}

	return sanitized
}

// NormalizeInterfaceName normalizes an interface name to a standard format
func NormalizeInterfaceName(name string) string {
	// Convert to lowercase and sanitize
	normalized := strings.ToLower(name)
	return SanitizeInterfaceName(normalized)
}
