// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"strings"
	"sync"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// GVK is the (Group, Kind) pair the router keys on. We deliberately
// drop Version: rule packs declare interest at the API-group level,
// and an OpenShift v1 vs v2 split would be addressed via separate
// modules in the same pack rather than two router entries.
type GVK struct {
	Group string
	Kind  string
}

// GVKOf returns the GVK of a resource. graph.Resource keeps the API
// group inside GroupVersion as "<group>/<version>"; core/v1 resources
// have an empty group, which is the empty string here too.
func GVKOf(r graph.Resource) GVK {
	if r.GroupVersion == "" {
		return GVK{Kind: r.Kind}
	}
	if i := strings.Index(r.GroupVersion, "/"); i > 0 {
		return GVK{Group: r.GroupVersion[:i], Kind: r.Kind}
	}
	// "v1" / "v1beta1" with no slash is the core group.
	return GVK{Kind: r.Kind}
}

// Router maps GVKs to the modules a rule pack registered for that
// GVK. Lookup is O(1); construction is one allocation per pack
// regardless of pack size.
//
// A module whose Match has both Group and Kind empty matches every
// resource — currently unused in shipped rule packs but kept open
// for built-in catch-all uses. The router stores those under a
// dedicated wildcard slice that Match unions in for every lookup.
type Router struct {
	mu       sync.RWMutex
	byGVK    map[GVK][]*ModuleSpec
	wildcard []*ModuleSpec
}

// NewRouter returns an empty router. Add modules via Register; build
// from a single rule pack via FromRulePack for the common case.
func NewRouter() *Router {
	return &Router{byGVK: make(map[GVK][]*ModuleSpec)}
}

// FromRulePacks builds a router that routes every module in every
// pack. Used by the bootstrap path that wires several packs at once.
func FromRulePacks(packs ...*RulePack) *Router {
	r := NewRouter()
	for _, p := range packs {
		if p == nil {
			continue
		}
		for _, m := range p.Modules {
			r.Register(p.Name, m)
		}
	}
	return r
}

// Register adds a module under its declared GVKMatch. The packName
// prefix matches the engine's "<pack>/<module>" registration key so
// Match callers can use the returned ModuleSpec.Name directly when
// looking up the engine's prepared query.
func (r *Router) Register(packName string, m *ModuleSpec) {
	if m == nil {
		return
	}
	// Snapshot the module with its namespaced key so Match callers
	// can pass m.Name straight to Engine.Evaluate.
	namespaced := *m
	namespaced.Name = packName + "/" + m.Name

	r.mu.Lock()
	defer r.mu.Unlock()
	if m.Match.Group == "" && m.Match.Kind == "" {
		r.wildcard = append(r.wildcard, &namespaced)
		return
	}
	key := GVK{Group: m.Match.Group, Kind: m.Match.Kind}
	r.byGVK[key] = append(r.byGVK[key], &namespaced)
}

// Match returns every module whose declared GVKMatch covers gvk.
// Wildcard (empty Group + Kind) modules are unioned in. The slice
// is freshly allocated; callers may mutate it without disturbing
// router state.
func (r *Router) Match(gvk GVK) []*ModuleSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	exact := r.byGVK[gvk]
	if len(exact) == 0 && len(r.wildcard) == 0 {
		return nil
	}
	out := make([]*ModuleSpec, 0, len(exact)+len(r.wildcard))
	out = append(out, exact...)
	out = append(out, r.wildcard...)
	return out
}

// Size reports the total number of registered modules. Used by the
// /metrics readiness check + tests.
func (r *Router) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := len(r.wildcard)
	for _, ms := range r.byGVK {
		n += len(ms)
	}
	return n
}
