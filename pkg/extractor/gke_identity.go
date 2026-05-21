// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// GKEIdentityExtractor emits BINDS_PLATFORM_IDENTITY edges from GKE
// Workload Identity Kubernetes metadata to a synthetic
// ExternalIdentity endpoint that represents a GCP service account
// (F-209.3, P3-T25).
//
// v1.3 covers the GKE Workload Identity annotation: a
// ServiceAccount with `iam.gke.io/gcp-service-account=<email>` is
// bound to the named GCP service account by the GKE identity
// federation webhook.
//
// KubeAtlas does not call any GCP SDK to validate the email
// (invariant 2.7); the edge is derived purely from the K8s
// annotation.
type GKEIdentityExtractor struct{}

// gkeWorkloadIdentityAnnotation is the GKE Workload Identity
// annotation the GKE Pod Identity Webhook consumes.
const gkeWorkloadIdentityAnnotation = "iam.gke.io/gcp-service-account"

// GKEServiceAccountPlatform tags ExternalIdentity ids the GKE
// extractor produces. Stored as the "kind" segment of the synthetic
// id so the UI can distinguish a GCP service-account binding from
// an EKS IAM role or AKS managed identity.
const GKEServiceAccountPlatform = "GCPServiceAccount"

// Type implements Extractor.
func (GKEIdentityExtractor) Type() graph.EdgeType {
	return graph.EdgeTypeBindsPlatformIdentity
}

// Extract emits one edge per ServiceAccount carrying the GKE
// Workload Identity annotation. No-op on non-SA resources or SAs
// without the annotation so the extractor is safe to register
// unconditionally on non-GKE clusters.
func (GKEIdentityExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "ServiceAccount" {
		return nil, nil
	}
	gcpSA := strings.TrimSpace(r.Annotations[gkeWorkloadIdentityAnnotation])
	if gcpSA == "" {
		return nil, nil
	}
	to := externalIdentityID(r.ClusterID, GKEServiceAccountPlatform, gcpSA)
	return []graph.Edge{{
		From: r.ID(),
		To:   to,
		Type: graph.EdgeTypeBindsPlatformIdentity,
	}}, nil
}
