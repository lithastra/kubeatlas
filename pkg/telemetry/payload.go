// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package telemetry implements KubeAtlas's opt-in, anonymous usage
// reporting. It is OFF by default and sends only coarse, non-identifying
// data — version, K8s version, OS/arch, tier, a resource-count bucket,
// enabled rule-pack names, and cluster counts — to a single hard-coded
// endpoint the project operates. It never records resource names,
// namespaces, label values, IPs, or any identifier that could correlate
// two sessions (invariant 2.3).
package telemetry

// SchemaVersion is the telemetry payload schema version. Any change to
// the Payload fields must bump this and update
// docs/concepts/telemetry-schema.md in the same change.
const SchemaVersion = "1.0"

// Payload is exactly what a report sends. Every field is coarse and
// non-identifying by construction; adding a field requires a human
// review plus a SchemaVersion bump (invariant 2.3).
type Payload struct {
	SchemaVersion    string `json:"schema_version"`
	KubeAtlasVersion string `json:"kubeatlas_version"`
	K8sVersion       string `json:"k8s_version"`
	OS               string `json:"os"`   // linux / darwin / windows
	Arch             string `json:"arch"` // amd64 / arm64
	Tier             string `json:"tier"` // memory / postgres

	// ResourceBucket is the order-of-magnitude band of the graph size,
	// never the exact count: "<1K" | "1K-5K" | "5K-10K" | ">10K".
	ResourceBucket string `json:"resource_bucket"`

	// EnabledPacks lists rule-pack names (no versions, no contents).
	EnabledPacks []string `json:"enabled_packs"`

	ClusterCount int `json:"cluster_count"`

	// PlatformDistribution counts clusters by platform family
	// (vanilla / openshift / eks / aks / gke) — counts only, no names.
	PlatformDistribution map[string]int `json:"platform_distribution"`

	// SessionNonce is random per process start and never persisted. It
	// lets the receiver de-duplicate a single session's repeated
	// reports without being able to correlate across restarts
	// (invariant 2.3: no cross-session identifier).
	SessionNonce string `json:"session_nonce"`
}

// resourceBucket maps an exact resource count to its coarse band.
func resourceBucket(n int) string {
	switch {
	case n < 1000:
		return "<1K"
	case n < 5000:
		return "1K-5K"
	case n < 10000:
		return "5K-10K"
	default:
		return ">10K"
	}
}
