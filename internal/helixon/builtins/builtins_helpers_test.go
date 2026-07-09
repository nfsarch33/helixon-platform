package builtins

import (
	"strings"
	"testing"
)

// TestValidateAllowedPath covers the v17206 extracted helper.
// TDD-first: tests written before refactor; behaviour preserved.
func TestValidateAllowedPath(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		allowedPaths []string
		wantErr      bool
		errSubstr    string
	}{
		{
			name:         "no allowed paths configured = always allowed",
			path:         "/anything",
			allowedPaths: nil,
			wantErr:      false,
		},
		{
			name:         "empty allowed paths slice = always allowed",
			path:         "/anything",
			allowedPaths: []string{},
			wantErr:      false,
		},
		{
			name:         "path matches prefix = allowed",
			path:         "/home/user/data/file.txt",
			allowedPaths: []string{"/home/user/data"},
			wantErr:      false,
		},
		{
			name:         "path does not match any prefix = rejected",
			path:         "/etc/passwd",
			allowedPaths: []string{"/home/user/data"},
			wantErr:      true,
			errSubstr:    "not within allowed directories",
		},
		{
			name:         "multiple prefixes, second matches",
			path:         "/var/data/file.txt",
			allowedPaths: []string{"/home", "/var/data"},
			wantErr:      false,
		},
		{
			name:         "exact match allowed",
			path:         "/home",
			allowedPaths: []string{"/home"},
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAllowedPath(tt.path, tt.allowedPaths)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAllowedPath(%q, %v) error = %v, wantErr %v", tt.path, tt.allowedPaths, err, tt.wantErr)
			}
			if tt.wantErr && tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Fatalf("validateAllowedPath error %q does not contain %q", err.Error(), tt.errSubstr)
			}
		})
	}
}
