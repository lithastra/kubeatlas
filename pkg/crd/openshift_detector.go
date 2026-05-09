// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package crd

import (
	"errors"
	"fmt"

	"k8s.io/client-go/discovery"
)

// openShiftAPIGroup is the marker the detector keys on. It is the
// most stable signal across OKD / OCP 4.x / OpenShift Local (CRC):
// every variant exposes route.openshift.io. Newer / older versions
// can change everything else (OAuth flows, console namespace, SCC
// schema), but Routes have shipped under this group since the
// project began (guide §2.4: detector looks at API group only).
const openShiftAPIGroup = "route.openshift.io"

// DetectOpenShift returns true when route.openshift.io is exposed on
// the cluster. It does no version analysis, makes no write calls,
// and never returns an error AND a true together — those branches
// are for callers that want to log "detector failed; assuming non-
// OpenShift" without conflating the two outcomes (anti-pattern #35).
func DetectOpenShift(client discovery.DiscoveryInterface) (bool, error) {
	if client == nil {
		return false, errors.New("DetectOpenShift: nil discovery client")
	}
	groups, err := client.ServerGroups()
	if err != nil {
		return false, fmt.Errorf("DetectOpenShift: list server groups: %w", err)
	}
	if groups == nil {
		return false, nil
	}
	for _, g := range groups.Groups {
		if g.Name == openShiftAPIGroup {
			return true, nil
		}
	}
	return false, nil
}

// RulePackMode is the tri-state knob the operator sets via
// KUBEATLAS_RULEPACK_OPENSHIFT (Helm rulePacks.openshift). It
// decouples "should we load the openshift pack" from "is this
// actually an OpenShift cluster" — the latter question is wrong
// when the user has a kind cluster they want to test rules against.
type RulePackMode string

const (
	// RulePackModeAuto runs the detector and loads the pack when
	// route.openshift.io is present. The default.
	RulePackModeAuto RulePackMode = "auto"
	// RulePackModeOn forces the pack to load regardless of
	// detection. Useful for offline rule development against a
	// non-OpenShift cluster (kind + raw Route YAML).
	RulePackModeOn RulePackMode = "true"
	// RulePackModeOff suppresses the pack regardless of detection.
	// Useful when an operator wants to swap in a fork via the
	// extras list (P2-T13).
	RulePackModeOff RulePackMode = "false"
)

// ParseRulePackMode normalizes the value the operator supplied. An
// empty string falls back to auto so a user who set
// rulePacks.openshift to "" gets the documented default rather than
// a surprise error.
func ParseRulePackMode(s string) (RulePackMode, error) {
	switch s {
	case "", "auto":
		return RulePackModeAuto, nil
	case "true":
		return RulePackModeOn, nil
	case "false":
		return RulePackModeOff, nil
	default:
		return "", fmt.Errorf("invalid RulePackMode %q: want auto|true|false", s)
	}
}

// ResolveOpenShiftLoad combines the operator's mode setting with the
// detector outcome and returns the final boolean: should we load
// the openshift pack? Detector errors under "auto" log-and-degrade
// to false (anti-pattern #35).
func ResolveOpenShiftLoad(mode RulePackMode, client discovery.DiscoveryInterface) (load bool, detectErr error) {
	switch mode {
	case RulePackModeOn:
		return true, nil
	case RulePackModeOff:
		return false, nil
	case RulePackModeAuto:
		ok, err := DetectOpenShift(client)
		if err != nil {
			// Caller logs at warn; we still return false so the
			// boot path continues without the pack.
			return false, err
		}
		return ok, nil
	default:
		return false, fmt.Errorf("ResolveOpenShiftLoad: unknown mode %q", mode)
	}
}
