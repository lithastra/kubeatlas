// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import "strings"

// ReferenceOnlyAnnotation marks a synthetic node derived from a reference in
// another Kubernetes object. KubeAtlas deliberately does not read Secret
// objects, so a reference-only Secret node proves only that a workload names
// it; it does not prove that the Secret exists.
const ReferenceOnlyAnnotation = "kubeatlas.io/reference-only"

// SecretReferenceResource returns the only Secret representation KubeAtlas is
// allowed to retain: namespace, name, cluster identity, and a generated marker.
// It intentionally excludes UID, labels, owner references, resource version,
// source annotations, type, and Raw payload.
func SecretReferenceResource(namespace, name, clusterID string) Resource {
	return Resource{
		Kind:      "Secret",
		Name:      name,
		Namespace: namespace,
		Annotations: map[string]string{
			ReferenceOnlyAnnotation: "true",
		},
		ClusterID: clusterID,
	}
}

// SanitizeResource applies the storage and evaluation boundary for Kubernetes
// Secrets. Non-Secret resources are returned unchanged: KubeAtlas intentionally
// preserves their Raw payloads, including ConfigMap values. Secret resources
// are reduced to reference-only nodes before they can reach a store, extractor,
// API, diagnostic, export, or telemetry path.
func SanitizeResource(r Resource) Resource {
	if r.Kind != "Secret" {
		return r
	}
	return SecretReferenceResource(r.Namespace, r.Name, r.ClusterID)
}

// MetadataOnlyEvent removes resource payloads from snapshot history. The
// append-only event stream records identity and timing only for every kind.
func MetadataOnlyEvent(e ResourceEvent) ResourceEvent {
	e.Data = nil
	return e
}

// SecretReferenceFromEdge parses the canonical target ID of any edge whose
// destination is a Secret. Built-in edges use USES_SECRET; rule packs may use
// a more specific type such as STORES_IN. It lets every GraphStore materialise
// the reference-only endpoint even when the edge comes from the offline
// collector, a CRD rule, or another caller outside the live informer pipeline.
func SecretReferenceFromEdge(e Edge) (Resource, bool) {
	target := e.To
	clusterID := ""
	if before, after, ok := strings.Cut(target, ":"); ok {
		clusterID = before
		target = after
	}
	parts := strings.SplitN(target, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] != "Secret" || parts[2] == "" {
		return Resource{}, false
	}
	return SecretReferenceResource(parts[0], parts[2], clusterID), true
}
