// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BindOrigin binds a type token to one metadata owner and registration lifetime.
// It records no objects and shares the existing bounded type inventory. Missing
// owner metadata does not disable collection; it simply cannot attest origin.
func (s *CoverageSession) BindOrigin(owner graph.ResourceOrigin) bool {
	if s == nil || s.parent == nil {
		return false
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return false
	}
	owner.ClusterID, owner.Source, owner.Registration = s.cluster, string(s.source), hex.EncodeToString(token[:])
	owner.ObjectUID, owner.ObjectVersion = "", ""
	if !owner.Valid() {
		return false
	}
	s.tracker.mu.Lock()
	defer s.tracker.mu.Unlock()
	if !s.activeLocked() {
		return false
	}
	r := s.resources[owner.GVR()]
	if r == nil || !s.ownsTypeLocked(owner.GVR(), r) || r.origin.Valid() {
		return false
	}
	r.origin = owner
	return true
}

// StampOrigin supplies only this live token's exact type/scope receipt. Retired,
// replaced, unowned and foreign-scope callbacks cannot stamp a new owner.
func (s *CoverageSession) StampOrigin(r graph.Resource) graph.Resource {
	r.Origin = graph.ResourceOrigin{}
	if s == nil {
		return r
	}
	s.tracker.mu.RLock()
	defer s.tracker.mu.RUnlock()
	if !s.activeLocked() || r.ClusterID != s.cluster {
		return r
	}
	gv, err := schema.ParseGroupVersion(r.GroupVersion)
	if err != nil {
		return r
	}
	for gvr, row := range s.resources {
		origin := row.origin
		origin.ObjectUID, origin.ObjectVersion = string(r.UID), r.ResourceVersion
		if gvr.GroupVersion() == gv && s.ownsTypeLocked(gvr, row) && !row.stopped && origin.Matches(r) {
			r.Origin = origin
			break
		}
	}
	return r
}
