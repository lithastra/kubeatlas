// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"runtime"
)

// Providers supplies the live values a Payload needs. Each is optional;
// a nil provider yields the field's zero value. Keeping these as
// closures rather than a concrete dependency keeps the package free of
// imports on the store / rego / multicluster packages.
type Providers struct {
	K8sVersion    func() string
	Tier          func() string
	ResourceCount func(context.Context) (int, error)
	EnabledPacks  func() []string
	ClusterCount  func() int
	Platforms     func() map[string]int
}

// Collector assembles a Payload on demand. The session nonce is
// generated once at construction and never persisted, so it cannot
// correlate two process lifetimes (invariant 2.3).
type Collector struct {
	version   string
	providers Providers
	nonce     string
}

// NewCollector builds a collector for the given KubeAtlas version.
func NewCollector(version string, p Providers) *Collector {
	return &Collector{version: version, providers: p, nonce: newNonce()}
}

// Collect builds the Payload that would be sent right now. It is pure
// with respect to the process (no network, no mutation) so the
// /preview endpoint can call it freely.
func (c *Collector) Collect(ctx context.Context) Payload {
	n := 0
	if c.providers.ResourceCount != nil {
		if got, err := c.providers.ResourceCount(ctx); err == nil {
			n = got
		}
	}

	packs := []string{}
	if c.providers.EnabledPacks != nil {
		if got := c.providers.EnabledPacks(); got != nil {
			packs = got
		}
	}
	platforms := map[string]int{}
	if c.providers.Platforms != nil {
		if got := c.providers.Platforms(); got != nil {
			platforms = got
		}
	}

	return Payload{
		SchemaVersion:        SchemaVersion,
		KubeAtlasVersion:     c.version,
		K8sVersion:           callString(c.providers.K8sVersion),
		OS:                   runtime.GOOS,
		Arch:                 runtime.GOARCH,
		Tier:                 callString(c.providers.Tier),
		ResourceBucket:       resourceBucket(n),
		EnabledPacks:         packs,
		ClusterCount:         callInt(c.providers.ClusterCount),
		PlatformDistribution: platforms,
		SessionNonce:         c.nonce,
	}
}

func callString(f func() string) string {
	if f == nil {
		return ""
	}
	return f()
}

func callInt(f func() int) int {
	if f == nil {
		return 0
	}
	return f()
}

// newNonce returns a 128-bit random hex string. crypto/rand never fails
// in practice on the platforms KubeAtlas targets; on the vanishingly
// unlikely error we fall back to a fixed marker rather than panic.
func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}
