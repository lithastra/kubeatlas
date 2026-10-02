// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"slices"
	"strings"
	"time"
	"unicode"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	MaxInventoryGroupVersions = 64
	MaxInventoryResources     = 256
	APIInventoryMaxAge        = 10 * time.Minute
)

type APIInventoryState string

const (
	APIInventoryUnknown  APIInventoryState = "unknown"
	APIInventoryComplete APIInventoryState = "complete"
	APIInventoryPartial  APIInventoryState = "partial"
	APIInventoryDenied   APIInventoryState = "permission_denied"
	APIInventoryFailed   APIInventoryState = "failed"
)

// Describes an API endpoint, never an object, value, or effective permission.
type APIResourceDescriptor struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Resource   string `json:"resource"`
	Kind       string `json:"kind"`
	Namespaced bool   `json:"namespaced"`
	List       bool   `json:"list"`
	Watch      bool   `json:"watch"`
}

func (r APIResourceDescriptor) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
}

func ValidAPIResourceDescriptor(r APIResourceDescriptor) bool {
	return (r.Group == "" || len(validation.IsDNS1123Subdomain(r.Group)) == 0) &&
		len(validation.IsDNS1035Label(r.Version)) == 0 && len(validation.IsDNS1035Label(r.Resource)) == 0 &&
		r.Kind != "" && len(r.Kind) <= 253 && !strings.ContainsAny(r.Kind, "/:\\") &&
		!strings.ContainsFunc(r.Kind, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

type APIInventoryEvidence struct {
	ClusterID  string                  `json:"clusterId"`
	Generation uint64                  `json:"generation"`
	Revision   uint64                  `json:"revision"`
	State      APIInventoryState       `json:"state"`
	CheckedAt  time.Time               `json:"checkedAt"`
	Stale      bool                    `json:"stale"`
	Stopped    bool                    `json:"stopped"`
	Limited    bool                    `json:"limited"`
	Resources  []APIResourceDescriptor `json:"resources"`
}

type APIInventorySession struct {
	tracker  *CoverageTracker
	evidence APIInventoryEvidence
}

// Independent of core/source registrations: restarting a watch cannot make an
// old discovery check fresh. Callback ownership rejects replaced collectors.
func (t *CoverageTracker) BeginAPIInventory(cluster string) (*APIInventorySession, error) {
	if !validCoverageScope(cluster, nil) {
		return nil, ErrCoverageScope
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasClusterLocked(cluster) && t.clusterCountLocked() >= MaxCoverageClusters {
		return nil, ErrCoverageScope
	}
	t.nextGeneration++
	s := &APIInventorySession{tracker: t, evidence: APIInventoryEvidence{ClusterID: cluster, Generation: t.nextGeneration, State: APIInventoryUnknown, Resources: []APIResourceDescriptor{}}}
	t.inventories[cluster] = s
	return s, nil
}

func (s *APIInventorySession) Record(state APIInventoryState, resources []APIResourceDescriptor, limited bool, checked time.Time) error {
	if s == nil {
		return nil
	}
	if !ValidAPIInventoryState(state) || checked.IsZero() || state == APIInventoryUnknown || len(resources) > MaxInventoryResources || (state == APIInventoryComplete && limited) || ((state == APIInventoryDenied || state == APIInventoryFailed) && len(resources) != 0) {
		return ErrCoverageScope
	}
	owned := slices.Clone(resources)
	if owned == nil {
		owned = []APIResourceDescriptor{}
	}
	slices.SortFunc(owned, func(a, b APIResourceDescriptor) int { return strings.Compare(a.GVR().String(), b.GVR().String()) })
	versions := map[schema.GroupVersion]bool{}
	for i, r := range owned {
		if !ValidAPIResourceDescriptor(r) || (i > 0 && owned[i-1].GVR() == r.GVR()) {
			return ErrCoverageScope
		}
		versions[r.GVR().GroupVersion()] = true
	}
	if len(versions) > MaxInventoryGroupVersions {
		return ErrCoverageScope
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if s.evidence.Stopped || s.tracker.inventories[s.evidence.ClusterID] != s {
		return nil
	}
	s.evidence.State, s.evidence.CheckedAt, s.evidence.Limited = state, checked, limited
	s.evidence.Resources = owned
	s.evidence.Revision++
	return nil
}

func (s *APIInventorySession) Stop() {
	if s == nil {
		return
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if s.tracker.inventories[s.evidence.ClusterID] == s {
		s.evidence.Stopped = true
	}
}

func ValidAPIInventoryState(state APIInventoryState) bool {
	switch state {
	case APIInventoryUnknown, APIInventoryComplete, APIInventoryPartial, APIInventoryDenied, APIInventoryFailed:
		return true
	default:
		return false
	}
}

func CloneAPIInventory(evidence *APIInventoryEvidence) *APIInventoryEvidence {
	if evidence == nil {
		return nil
	}
	copy := *evidence
	copy.Resources = slices.Clone(evidence.Resources)
	return &copy
}
