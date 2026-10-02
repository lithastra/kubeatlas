// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ImpactSnapshotter is an optional capability, not a GraphStore v2 change.
// Implementations return an independently owned, point-in-time projection of
// exactly one cluster. Resources include identity, owner references, and narrowly
// allowlisted reference fields in Raw for supported non-Secret consumers only.
// That Raw is a partial projection, NOT a complete Kubernetes object. Full Raw,
// labels, source annotations, edge attributes, and cross-cluster edges are
// excluded. Same-cluster dangling tuples remain; Secrets stay reference-only.
// Legacy snapshots/serialization are unchanged. No partial graph on error.
//
// This does not authorize a cluster or guarantee collection freshness. Visibility
// wrappers must explicitly implement this capability with their restrictions;
// callers must not fall back to Snapshot or unwrap a restricted store.
type ImpactSnapshotter interface {
	SnapshotImpact(context.Context, ImpactSnapshotOptions) (*Graph, error)
}

const (
	ImpactReadTimeout = 5 * time.Second

	DefaultImpactResources = 20_000
	DefaultImpactEdges     = 100_000
	DefaultImpactBytes     = 16 << 20
	MaxImpactResources     = 100_000
	MaxImpactEdges         = 500_000
	MaxImpactBytes         = 64 << 20
)

var (
	ErrImpactSnapshotOptions = errors.New("impact snapshot: invalid options")
	ErrImpactSnapshotLimit   = errors.New("impact snapshot: projection budget exceeded")
)

// ImpactSnapshotOptions contains internal read budgets, not traversal limits.
// Zero values use the defaults. MaxBytes charges a conservative JSON-size upper
// bound for allowlisted metadata (including escape expansion), not exact Go heap
// usage or the final HTTP response size. All budgets fail closed, never truncate.
type ImpactSnapshotOptions struct {
	ClusterID    string
	MaxResources int
	MaxEdges     int
	MaxBytes     int
}

func (o ImpactSnapshotOptions) Normalize() (ImpactSnapshotOptions, error) {
	if o.MaxResources == 0 {
		o.MaxResources = DefaultImpactResources
	}
	if o.MaxEdges == 0 {
		o.MaxEdges = DefaultImpactEdges
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = DefaultImpactBytes
	}
	if strings.ContainsAny(o.ClusterID, "/:") || o.MaxResources < 1 || o.MaxResources > MaxImpactResources ||
		o.MaxEdges < 1 || o.MaxEdges > MaxImpactEdges || o.MaxBytes < 1 || o.MaxBytes > MaxImpactBytes {
		return o, ErrImpactSnapshotOptions
	}
	return o, nil
}

// ImpactIDInCluster tests the canonical ID's cluster prefix. Empty selects
// standalone IDs only. Missing endpoints are retained without fetching objects.
func ImpactIDInCluster(id, clusterID string) bool {
	head, _, path := strings.Cut(id, "/")
	if !path {
		return false
	}
	// RBAC names can contain colons (for example system:node). Only the
	// segment before the first slash can contain a cluster prefix.
	if prefix, _, present := strings.Cut(head, ":"); present {
		return prefix == clusterID && prefix != ""
	}
	return clusterID == ""
}

// ImpactProjectionBuilder applies the same allowlist, ownership, and budgets to
// both built-in adapters. It is local to a read, not safe for concurrent writers.
// Callers must discard it after an error; Graph may only be returned on success.
type ImpactProjectionBuilder struct {
	Graph     *Graph
	opts      ImpactSnapshotOptions
	remaining int
}

func NewImpactProjectionBuilder(opts ImpactSnapshotOptions) (*ImpactProjectionBuilder, error) {
	opts, err := opts.Normalize()
	if err != nil {
		return nil, err
	}
	return &ImpactProjectionBuilder{
		Graph: &Graph{Resources: []Resource{}, Edges: []Edge{}}, opts: opts, remaining: opts.MaxBytes,
	}, nil
}

func (b *ImpactProjectionBuilder) AddResource(ctx context.Context, r Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.ClusterID != b.opts.ClusterID {
		return nil
	}
	if len(b.Graph.Resources) == b.opts.MaxResources {
		return ErrImpactSnapshotLimit
	}
	r = SanitizeResource(r)
	// Charge before copying slices or encoding data. In particular, neither an
	// enormous scalar nor an owner-reference array gets an unbounded allocation.
	if err := b.charge(512, r.Kind, r.Name, r.Namespace, r.ClusterID, r.GroupVersion, string(r.UID), r.ResourceVersion); err != nil {
		return err
	}
	for _, owner := range r.OwnerReferences {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.charge(64, owner.Kind, owner.Name, string(owner.UID)); err != nil {
			return err
		}
	}
	projected := Resource{
		Kind: r.Kind, Name: r.Name, Namespace: r.Namespace, ClusterID: r.ClusterID,
		UID: r.UID, ResourceVersion: r.ResourceVersion, GroupVersion: r.GroupVersion,
	}
	if len(r.OwnerReferences) != 0 {
		projected.OwnerReferences = append([]OwnerRef(nil), r.OwnerReferences...)
	}
	var err error
	projected.Raw, err = b.projectReferenceFields(ctx, r)
	if err != nil {
		return err
	}
	b.Graph.Resources = append(b.Graph.Resources, SanitizeResource(projected))
	return nil
}

func (b *ImpactProjectionBuilder) AddEdge(ctx context.Context, e Edge) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ImpactIDInCluster(e.From, b.opts.ClusterID) || !ImpactIDInCluster(e.To, b.opts.ClusterID) {
		return nil
	}
	if len(b.Graph.Edges) == b.opts.MaxEdges {
		return ErrImpactSnapshotLimit
	}
	if err := b.charge(96, e.From, e.To, string(e.Type)); err != nil {
		return err
	}
	b.Graph.Edges = append(b.Graph.Edges, Edge{From: e.From, To: e.To, Type: e.Type})
	return nil
}

func (b *ImpactProjectionBuilder) charge(overhead int, fields ...string) error {
	if overhead > b.remaining {
		return ErrImpactSnapshotLimit
	}
	b.remaining -= overhead
	for _, field := range fields {
		// JSON's largest per-byte escape is six bytes. Division before
		// multiplication avoids overflowing on adversarial string lengths.
		if len(field) > b.remaining/6 {
			return ErrImpactSnapshotLimit
		}
		b.remaining -= len(field) * 6
	}
	return nil
}
