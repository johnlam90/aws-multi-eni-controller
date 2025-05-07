package util

import (
	"testing"
)

func TestGetInstanceIDFromProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       string
	}{
		{
			name:       "valid AWS provider ID",
			providerID: "aws:///us-west-2a/i-0123456789abcdef0",
			want:       "i-0123456789abcdef0",
		},
		{
			name:       "valid AWS provider ID with different format",
			providerID: "aws://us-west-2/i-0123456789abcdef0",
			want:       "i-0123456789abcdef0",
		},
		{
			name:       "empty provider ID",
			providerID: "",
			want:       "",
		},
		{
			name:       "invalid provider ID format",
			providerID: "aws",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetInstanceIDFromProviderID(tt.providerID); got != tt.want {
				t.Errorf("GetInstanceIDFromProviderID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestContainsString(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     string
		want  bool
	}{
		{
			name:  "string exists in slice",
			slice: []string{"a", "b", "c"},
			s:     "b",
			want:  true,
		},
		{
			name:  "string does not exist in slice",
			slice: []string{"a", "b", "c"},
			s:     "d",
			want:  false,
		},
		{
			name:  "empty slice",
			slice: []string{},
			s:     "a",
			want:  false,
		},
		{
			name:  "nil slice",
			slice: nil,
			s:     "a",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsString(tt.slice, tt.s); got != tt.want {
				t.Errorf("ContainsString() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRemoveString(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     string
		want  []string
	}{
		{
			name:  "remove existing string",
			slice: []string{"a", "b", "c"},
			s:     "b",
			want:  []string{"a", "c"},
		},
		{
			name:  "remove non-existing string",
			slice: []string{"a", "b", "c"},
			s:     "d",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "remove from empty slice",
			slice: []string{},
			s:     "a",
			want:  []string{},
		},
		{
			name:  "remove from nil slice",
			slice: nil,
			s:     "a",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveString(tt.slice, tt.s)

			// Check length
			if len(got) != len(tt.want) {
				t.Errorf("RemoveString() length = %v, want %v", len(got), len(tt.want))
				return
			}

			// Check contents
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("RemoveString()[%d] = %v, want %v", i, v, tt.want[i])
				}
			}
		})
	}
}

func TestMergeMaps(t *testing.T) {
	tests := []struct {
		name string
		m1   map[string]string
		m2   map[string]string
		want map[string]string
	}{
		{
			name: "merge non-overlapping maps",
			m1:   map[string]string{"a": "1", "b": "2"},
			m2:   map[string]string{"c": "3", "d": "4"},
			want: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"},
		},
		{
			name: "merge with overlapping keys",
			m1:   map[string]string{"a": "1", "b": "2"},
			m2:   map[string]string{"b": "3", "c": "4"},
			want: map[string]string{"a": "1", "b": "3", "c": "4"},
		},
		{
			name: "merge with empty first map",
			m1:   map[string]string{},
			m2:   map[string]string{"a": "1", "b": "2"},
			want: map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "merge with empty second map",
			m1:   map[string]string{"a": "1", "b": "2"},
			m2:   map[string]string{},
			want: map[string]string{"a": "1", "b": "2"},
		},
		{
			name: "merge with nil maps",
			m1:   nil,
			m2:   nil,
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeMaps(tt.m1, tt.m2)

			// Check length
			if len(got) != len(tt.want) {
				t.Errorf("MergeMaps() length = %v, want %v", len(got), len(tt.want))
				return
			}

			// Check contents
			for k, v := range got {
				if wv, ok := tt.want[k]; !ok || v != wv {
					t.Errorf("MergeMaps()[%s] = %v, want %v", k, v, wv)
				}
			}
		})
	}
}
