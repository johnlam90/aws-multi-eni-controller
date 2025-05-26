package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestIsPCIDeviceBoundToDPDKFixed tests the fixed DPDK verification logic
func TestIsPCIDeviceBoundToDPDKFixed(t *testing.T) {
	t.Log("=== Testing Fixed DPDK Verification Logic ===")

	// Mock DPDK binding script output (similar to what we see in the cluster)
	mockOutput := `
Network devices using DPDK-compatible driver
============================================
0000:00:07.0 'Elastic Network Adapter (ENA) ec20' drv=vfio-pci unused=
0000:00:08.0 'Elastic Network Adapter (ENA) ec20' drv=vfio-pci unused=

Network devices using kernel driver
===================================
0000:00:05.0 'Elastic Network Adapter (ENA) ec20' if=eth0 drv=ena unused=vfio-pci *Active*
0000:00:06.0 'Elastic Network Adapter (ENA) ec20' if=eth1 drv=ena unused=vfio-pci

No 'Baseband' devices detected
==============================

No 'Crypto' devices detected
============================
`

	// Test the regex pattern extraction
	t.Log("1. Testing DPDK section extraction")

	// Test the new parsing logic (same as in main.go)
	dpdkHeaderRegex := regexp.MustCompile(`Network devices using DPDK-compatible driver\s*\n=+`)
	headerMatch := dpdkHeaderRegex.FindStringIndex(mockOutput)

	if headerMatch == nil {
		t.Fatal("No DPDK section header found in mock output")
	}

	// Find the start of the content after the header
	contentStart := headerMatch[1]

	// Find the end of the DPDK section (next section header or end of string)
	nextSectionRegex := regexp.MustCompile(`\n\n[A-Z][^=]*\n=+`)
	nextSectionMatch := nextSectionRegex.FindStringIndex(mockOutput[contentStart:])

	var dpdkSection string
	if nextSectionMatch != nil {
		// Extract content up to the next section
		dpdkSection = mockOutput[contentStart : contentStart+nextSectionMatch[0]]
	} else {
		// Extract content to the end of the string
		dpdkSection = mockOutput[contentStart:]
	}

	// Clean up the section (remove leading/trailing whitespace and empty lines)
	dpdkSection = strings.TrimSpace(dpdkSection)
	t.Logf("Extracted DPDK section: %q", dpdkSection)

	// Verify the section contains the expected devices
	expectedDevices := []string{"0000:00:07.0", "0000:00:08.0"}
	for _, device := range expectedDevices {
		if !regexp.MustCompile(device).MatchString(dpdkSection) {
			t.Errorf("Expected device %s not found in DPDK section", device)
		}
	}

	// Test PCI device matching
	t.Log("2. Testing PCI device and driver matching")

	testCases := []struct {
		pciAddress  string
		driver      string
		expected    bool
		description string
	}{
		{"0000:00:07.0", "vfio-pci", true, "Device bound to correct driver"},
		{"0000:00:08.0", "vfio-pci", true, "Second device bound to correct driver"},
		{"0000:00:07.0", "ena", false, "Device bound to different driver"},
		{"0000:00:05.0", "vfio-pci", false, "Device not in DPDK section"},
		{"0000:00:99.0", "vfio-pci", false, "Non-existent device"},
	}

	for _, tc := range testCases {
		t.Logf("Testing: %s", tc.description)

		// Test the PCI regex matching
		pciRegex := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(tc.pciAddress) + `.*?drv=` + regexp.QuoteMeta(tc.driver))
		result := pciRegex.MatchString(dpdkSection)

		if result != tc.expected {
			t.Errorf("PCI matching failed for %s with driver %s: expected %v, got %v",
				tc.pciAddress, tc.driver, tc.expected, result)
		} else {
			t.Logf("✓ Correct result for %s: %v", tc.pciAddress, result)
		}
	}

	t.Log("✓ Fixed DPDK verification logic test completed successfully")
}

// TestDPDKVerificationRegexPatterns tests various regex patterns
func TestDPDKVerificationRegexPatterns(t *testing.T) {
	t.Log("=== Testing DPDK Verification Regex Patterns ===")

	// Test different output formats that might be encountered
	testOutputs := []struct {
		name            string
		output          string
		expectedDevices map[string]string // pci -> driver
	}{
		{
			name: "Standard ENA output",
			output: `
Network devices using DPDK-compatible driver
============================================
0000:00:07.0 'Elastic Network Adapter (ENA) ec20' drv=vfio-pci unused=
0000:00:08.0 'Elastic Network Adapter (ENA) ec20' drv=vfio-pci unused=

Network devices using kernel driver
===================================
0000:00:05.0 'Elastic Network Adapter (ENA) ec20' if=eth0 drv=ena unused=vfio-pci *Active*
`,
			expectedDevices: map[string]string{
				"0000:00:07.0": "vfio-pci",
				"0000:00:08.0": "vfio-pci",
			},
		},
		{
			name: "Empty DPDK section",
			output: `Network devices using DPDK-compatible driver
============================================

Network devices using kernel driver
===================================
0000:00:05.0 'Elastic Network Adapter (ENA) ec20' if=eth0 drv=ena unused=vfio-pci *Active*
`,
			expectedDevices: map[string]string{},
		},
		{
			name: "Multiple driver types",
			output: `
Network devices using DPDK-compatible driver
============================================
0000:00:07.0 'Elastic Network Adapter (ENA) ec20' drv=vfio-pci unused=
0000:00:08.0 'Elastic Network Adapter (ENA) ec20' drv=uio_pci_generic unused=

Network devices using kernel driver
===================================
0000:00:05.0 'Elastic Network Adapter (ENA) ec20' if=eth0 drv=ena unused=vfio-pci *Active*
`,
			expectedDevices: map[string]string{
				"0000:00:07.0": "vfio-pci",
				"0000:00:08.0": "uio_pci_generic",
			},
		},
	}

	for _, testCase := range testOutputs {
		t.Logf("Testing output format: %s", testCase.name)

		// Extract DPDK section using the same logic as main.go
		dpdkHeaderRegex := regexp.MustCompile(`Network devices using DPDK-compatible driver\s*\n=+`)
		headerMatch := dpdkHeaderRegex.FindStringIndex(testCase.output)

		if headerMatch == nil {
			if len(testCase.expectedDevices) == 0 {
				// Expected for empty section test case
				continue
			}
			t.Errorf("No DPDK section header found for test case: %s", testCase.name)
			continue
		}

		// Find the start of the content after the header
		contentStart := headerMatch[1]

		// Find the end of the DPDK section (next section header or end of string)
		nextSectionRegex := regexp.MustCompile(`\n\n[A-Z][^=]*\n=+`)
		nextSectionMatch := nextSectionRegex.FindStringIndex(testCase.output[contentStart:])

		var dpdkSection string
		if nextSectionMatch != nil {
			// Extract content up to the next section
			dpdkSection = testCase.output[contentStart : contentStart+nextSectionMatch[0]]
		} else {
			// Extract content to the end of the string
			dpdkSection = testCase.output[contentStart:]
		}

		// Clean up the section (remove leading/trailing whitespace and empty lines)
		dpdkSection = strings.TrimSpace(dpdkSection)

		if len(testCase.expectedDevices) == 0 {
			// Expect empty DPDK section
			if dpdkSection != "" {
				t.Errorf("Expected empty DPDK section but got: %q", dpdkSection)
			}
			continue
		}

		// Test each expected device
		for pciAddress, expectedDriver := range testCase.expectedDevices {
			pciRegex := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(pciAddress) + `.*?drv=` + regexp.QuoteMeta(expectedDriver))
			if !pciRegex.MatchString(dpdkSection) {
				t.Errorf("Device %s with driver %s not found in DPDK section for test case %s",
					pciAddress, expectedDriver, testCase.name)
			} else {
				t.Logf("✓ Found device %s with driver %s", pciAddress, expectedDriver)
			}
		}
	}

	t.Log("✓ DPDK verification regex patterns test completed")
}
