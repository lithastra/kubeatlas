// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import "time"

// This is evidence about templates.gatekeeper.sh/v1/constrainttemplates only,
// not installation state, watch permission, or a complete API inventory.
type SourceDiscoveryState string

const (
	SourceAPIUnknown       SourceDiscoveryState = "unknown"
	SourceAPIAdvertised    SourceDiscoveryState = "advertised"
	SourceAPINotAdvertised SourceDiscoveryState = "not_advertised"
	SourceAPIDenied        SourceDiscoveryState = "permission_denied"
	SourceAPIFailed        SourceDiscoveryState = "failed"
	// Existing background discovery polls every five minutes while waiting.
	SourceDiscoveryMaxAge = 10 * time.Minute
)

type SourceDiscoveryEvidence struct {
	State     SourceDiscoveryState `json:"state"`
	CheckedAt time.Time            `json:"checkedAt"`
	Revision  uint64               `json:"revision"`
	Stale     bool                 `json:"stale"`
}

// RecordDiscovery records a sanitized result of existing background discovery.
// No errors, API bodies, object names, or credentials are retained. A missing
// discovery client stays unknown with a zero check time; no check is invented.
func (s *CoverageSession) RecordDiscovery(state SourceDiscoveryState, checked time.Time) error {
	if s == nil {
		return nil
	}
	if s.parent != nil || s.source != CoverageSourceGatekeeper || !ValidSourceDiscoveryState(state) || (state == SourceAPIUnknown) != checked.IsZero() {
		return ErrCoverageScope
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if !s.activeLocked() {
		return nil
	}
	revision := uint64(1)
	if s.discovery != nil {
		revision = s.discovery.Revision + 1
	}
	s.discovery = &SourceDiscoveryEvidence{State: state, CheckedAt: checked, Revision: revision}
	return nil
}

func ValidSourceDiscoveryState(state SourceDiscoveryState) bool {
	switch state {
	case SourceAPIUnknown, SourceAPIAdvertised, SourceAPINotAdvertised, SourceAPIDenied, SourceAPIFailed:
		return true
	default:
		return false
	}
}
