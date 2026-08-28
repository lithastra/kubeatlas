package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestConfigureGoMemoryLimit(t *testing.T) {
	previous := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(previous) })

	tests := []struct {
		name       string
		env        map[string]string
		wantLimit  int64
		configured bool
		wantError  string
	}{
		{name: "not configured"},
		{
			name: "default chart boundary",
			env: map[string]string{
				containerMemoryLimitEnv: "536870912",
				goMemoryLimitPercentEnv: "75",
			},
			wantLimit: 402653184, configured: true,
		},
		{
			name: "production profile boundary",
			env: map[string]string{
				containerMemoryLimitEnv: "2147483648",
				goMemoryLimitPercentEnv: "75",
			},
			wantLimit: 1610612736, configured: true,
		},
		{
			name:      "missing percentage",
			env:       map[string]string{containerMemoryLimitEnv: "536870912"},
			wantError: "must be set together",
		},
		{
			name: "invalid limit",
			env: map[string]string{
				containerMemoryLimitEnv: "not-bytes",
				goMemoryLimitPercentEnv: "75",
			},
			wantError: "positive integer",
		},
		{
			name: "unsafe percentage",
			env: map[string]string{
				containerMemoryLimitEnv: "536870912",
				goMemoryLimitPercentEnv: "91",
			},
			wantError: "50 through 90",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			gotLimit, gotConfigured, err := configureGoMemoryLimit(getenv)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("configureGoMemoryLimit() error = %v", err)
			}
			if gotLimit != tt.wantLimit || gotConfigured != tt.configured {
				t.Fatalf("configureGoMemoryLimit() = (%d, %v), want (%d, %v)", gotLimit, gotConfigured, tt.wantLimit, tt.configured)
			}
		})
	}
}
