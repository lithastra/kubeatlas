// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// AKSIdentityExtractor emits BINDS_PLATFORM_IDENTITY edges from
// Azure Workload Identity Kubernetes metadata to a synthetic
// ExternalIdentity endpoint that represents an Azure AD managed
// identity (F-209.2, P3-T24).
//
// v1.3 covers the modern Workload Identity webhook: a
// ServiceAccount with the `azure.workload.identity/client-id` label
// points at the AAD managed-identity GUID the AKS mutating webhook
// will exchange for tokens. The legacy AzureIdentity /
// AzureIdentityBinding CRDs (aad-pod-identity v1) are handled by a
// separate rego pack — the in-tree extractor sticks to the metadata
// path so it works on every cluster regardless of which CRDs are
// installed.
//
// KubeAtlas does not call any Azure SDK to validate the GUID
// (invariant 2.7); the edge is derived purely from the K8s label.
type AKSIdentityExtractor struct{}

// aksWorkloadIdentityLabel is the AKS Workload Identity webhook's
// signal that an SA should have AAD tokens injected.
const aksWorkloadIdentityLabel = "azure.workload.identity/client-id"

// AKSManagedIdentityPlatform tags ExternalIdentity ids the AKS
// extractor produces. Stored as the "kind" segment of the synthetic
// id so the UI can colour by platform without re-parsing the GUID.
const AKSManagedIdentityPlatform = "AADManagedIdentity"

// Type implements Extractor.
func (AKSIdentityExtractor) Type() graph.EdgeType {
	return graph.EdgeTypeBindsPlatformIdentity
}

// Extract emits one edge per ServiceAccount carrying the Workload
// Identity label. Non-SA resources, and SAs without the label, are
// no-ops, so the extractor is safe to register unconditionally on
// non-AKS clusters.
func (AKSIdentityExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "ServiceAccount" {
		return nil, nil
	}
	clientID := strings.TrimSpace(r.Labels[aksWorkloadIdentityLabel])
	if clientID == "" {
		return nil, nil
	}
	to := externalIdentityID(r.ClusterID, AKSManagedIdentityPlatform, clientID)
	return []graph.Edge{{
		From: r.ID(),
		To:   to,
		Type: graph.EdgeTypeBindsPlatformIdentity,
	}}, nil
}
