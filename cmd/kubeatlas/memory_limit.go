package main

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	containerMemoryLimitEnv = "KUBEATLAS_CONTAINER_MEMORY_LIMIT_BYTES"
	goMemoryLimitPercentEnv = "KUBEATLAS_GO_MEMORY_LIMIT_PERCENT"
)

// configureGoMemoryLimit derives a soft Go runtime-managed-memory boundary
// from the container's actual Kubernetes memory limit. The remaining process
// headroom covers memory the Go runtime cannot manage or release.
func configureGoMemoryLimit(getenv func(string) string) (int64, bool, error) {
	rawLimit := strings.TrimSpace(getenv(containerMemoryLimitEnv))
	rawPercent := strings.TrimSpace(getenv(goMemoryLimitPercentEnv))
	if rawLimit == "" && rawPercent == "" {
		return 0, false, nil
	}
	if rawLimit == "" || rawPercent == "" {
		return 0, false, fmt.Errorf("%s and %s must be set together", containerMemoryLimitEnv, goMemoryLimitPercentEnv)
	}

	limitBytes, err := strconv.ParseInt(rawLimit, 10, 64)
	if err != nil || limitBytes <= 0 {
		return 0, false, fmt.Errorf("%s must be a positive integer number of bytes", containerMemoryLimitEnv)
	}
	percent, err := strconv.ParseInt(rawPercent, 10, 64)
	if err != nil || percent < 50 || percent > 90 {
		return 0, false, fmt.Errorf("%s must be an integer from 50 through 90", goMemoryLimitPercentEnv)
	}

	// Split quotient and remainder so the multiplication cannot overflow while
	// still deriving the exact whole-byte floor for ordinary container limits.
	goLimitBytes := (limitBytes/100)*percent + ((limitBytes%100)*percent)/100
	if goLimitBytes <= 0 {
		return 0, false, fmt.Errorf("derived Go memory limit must be positive")
	}
	debug.SetMemoryLimit(goLimitBytes)
	return goLimitBytes, true, nil
}
