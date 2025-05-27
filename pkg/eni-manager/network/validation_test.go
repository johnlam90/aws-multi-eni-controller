package network

import (
	"testing"
)

func TestValidateInterfaceName(t *testing.T) {
	tests := []struct {
		name      string
		ifaceName string
		wantErr   bool
	}{
		{"valid eth interface", "eth0", false},
		{"valid ens interface", "ens5", false},
		{"valid with underscore", "test_iface", false},
		{"valid with dash", "test-iface", false},
		{"empty name", "", true},
		{"too long", "this_is_a_very_long_interface_name", true},
		{"invalid characters", "eth0@invalid", true},
		{"valid max length", "123456789012345", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInterfaceName(tt.ifaceName)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInterfaceName() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMTU(t *testing.T) {
	tests := []struct {
		name    string
		mtu     int
		wantErr bool
	}{
		{"valid standard MTU", 1500, false},
		{"valid jumbo MTU", 9000, false},
		{"minimum MTU", 68, false},
		{"maximum MTU", 9000, false},
		{"too small", 67, true},
		{"too large", 9001, true},
		{"zero", 0, true},
		{"negative", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMTU(tt.mtu)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMTU() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePCIAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"valid PCI address", "0000:00:06.0", false},
		{"valid with hex", "0000:0a:1f.7", false},
		{"valid uppercase", "0000:0A:1F.7", false},
		{"empty address", "", true},
		{"invalid format - missing colon", "000000:06.0", true},
		{"invalid format - missing dot", "0000:00:060", true},
		{"invalid format - too short", "0000:00:6.0", true},
		{"invalid format - too long", "00000:00:06.0", true},
		{"invalid characters", "0000:00:0g.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePCIAddress(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePCIAddress() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMACAddress(t *testing.T) {
	tests := []struct {
		name    string
		mac     string
		wantErr bool
	}{
		{"valid MAC colon format", "00:11:22:33:44:55", false},
		{"valid MAC dash format", "00-11-22-33-44-55", false},
		{"valid MAC dot format", "0011.2233.4455", false},
		{"empty MAC", "", true},
		{"invalid format", "00:11:22:33:44", true},
		{"invalid characters", "00:11:22:33:44:gg", true},
		{"too long", "00:11:22:33:44:55:66", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMACAddress(tt.mac)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMACAddress() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDeviceIndex(t *testing.T) {
	tests := []struct {
		name    string
		index   int
		wantErr bool
	}{
		{"valid index 0", 0, false},
		{"valid index 1", 1, false},
		{"valid index 15", 15, false},
		{"invalid negative", -1, true},
		{"invalid too large", 16, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDeviceIndex(tt.index)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDeviceIndex() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateENIPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"valid simple pattern", "eth[0-9]+", false},
		{"valid complex pattern", "^(eth|ens|eni)[0-9]+$", false},
		{"empty pattern", "", true},
		{"invalid regex", "[", true},
		{"invalid regex unclosed", "(eth", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateENIPattern(tt.pattern)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateENIPattern() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateInterfaceState(t *testing.T) {
	tests := []struct {
		name    string
		state   string
		wantErr bool
	}{
		{"valid UP", "UP", false},
		{"valid DOWN", "DOWN", false},
		{"valid UNKNOWN", "UNKNOWN", false},
		{"valid lowercase up", "up", false},
		{"valid mixed case", "Up", false},
		{"invalid state", "INVALID", true},
		{"empty state", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInterfaceState(tt.state)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInterfaceState() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseInterfaceIndex(t *testing.T) {
	tests := []struct {
		name      string
		ifaceName string
		want      int
		wantErr   bool
	}{
		{"eth0", "eth0", 0, false},
		{"eth1", "eth1", 1, false},
		{"ens5", "ens5", 5, false},
		{"ens10", "ens10", 10, false},
		{"no number", "eth", 0, true},
		{"no suffix", "interface", 0, true},
		{"mixed", "eth1abc", 0, true}, // No number at the end
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInterfaceIndex(tt.ifaceName)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseInterfaceIndex() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseInterfaceIndex() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsValidInterfaceForENI(t *testing.T) {
	tests := []struct {
		name       string
		ifaceName  string
		eniPattern string
		ignoreList []string
		want       bool
		wantErr    bool
	}{
		{"valid eth interface", "eth1", "^eth[0-9]+$", []string{"lo"}, true, false},
		{"valid ens interface", "ens5", "^(eth|ens)[0-9]+$", []string{"lo"}, true, false},
		{"ignored interface", "lo", "^eth[0-9]+$", []string{"lo"}, false, false},
		{"pattern mismatch", "wlan0", "^eth[0-9]+$", []string{}, false, false},
		{"invalid interface name", "eth@", "^eth[0-9]+$", []string{}, false, true},
		{"invalid pattern", "eth1", "[", []string{}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := IsValidInterfaceForENI(tt.ifaceName, tt.eniPattern, tt.ignoreList)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsValidInterfaceForENI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsValidInterfaceForENI() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizeInterfaceName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"valid name", "eth0", "eth0"},
		{"with invalid chars", "eth@0#", "eth_0_"},
		{"too long", "this_is_a_very_long_interface_name", "this_is_a_very_"},
		{"mixed invalid", "eth0!@#$%", "eth0_____"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeInterfaceName(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeInterfaceName() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNormalizeInterfaceName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"uppercase", "ETH0", "eth0"},
		{"mixed case", "EtH0", "eth0"},
		{"with invalid chars", "ETH@0", "eth_0"},
		{"already normalized", "eth0", "eth0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeInterfaceName(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeInterfaceName() = %v, want %v", got, tt.expected)
			}
		})
	}
}
