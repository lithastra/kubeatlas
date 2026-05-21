// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"context"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// EKSIdentityExtractor emits BINDS_PLATFORM_IDENTITY edges from EKS-
// specific Kubernetes metadata to a synthetic "ExternalIdentity"
// endpoint that represents an AWS IAM principal (F-209.1, P3-T23).
//
// v1.3 covers IRSA — a ServiceAccount with the
// `eks.amazonaws.com/role-arn` annotation points at the IAM role the
// pod webhook will mount credentials for. The aws-auth ConfigMap
// mapping is a separate follow-up commit (it requires YAML parsing
// of the ConfigMap's data fields and emits edges from RBAC User /
// Group endpoints rather than ServiceAccounts).
//
// KubeAtlas does not call any cloud SDK to validate the ARN
// (invariant 2.7); the edge is derived purely from the K8s
// annotation. The To endpoint is a synthetic dangling id with the
// shape "_external/IAMRole/<arn>" so two clusters referencing the
// same ARN never share a node in the federation view (per the
// guide's "do not merge ExternalIdentity across clusters" rule —
// the cluster prefix on the From SA's ID propagates naturally).
type EKSIdentityExtractor struct{}

// irsaAnnotation is the IRSA pod-identity-webhook annotation. The
// presence of this annotation on a ServiceAccount means the EKS
// mutating webhook will inject IAM credentials into Pods that mount
// the SA.
const irsaAnnotation = "eks.amazonaws.com/role-arn"

// ExternalIdentityKind is the synthetic Kind used in F-209 endpoint
// IDs. It is not a real K8s Kind — UI consumers detect it by the
// "_external" namespace marker and render an ExternalIdentity badge
// instead of a clickable resource link.
const ExternalIdentityKind = "ExternalIdentity"

// ExternalIdentityNamespace is the synthetic namespace under which
// every F-209 ExternalIdentity endpoint id is rooted.
const ExternalIdentityNamespace = "_external"

// EKSIRSARolePlatform tags ExternalIdentity ids created by this
// extractor. Stored as the "kind" segment of the synthetic id so the
// UI can colour by platform without re-parsing the ARN.
const EKSIRSARolePlatform = "IAMRole"

// Type implements Extractor.
func (EKSIdentityExtractor) Type() graph.EdgeType {
	return graph.EdgeTypeBindsPlatformIdentity
}

// Extract emits one edge per ServiceAccount carrying the IRSA
// annotation. Non-SA resources, and SAs without the annotation, are
// no-ops — the extractor is safe to register unconditionally on
// non-EKS clusters; it just never fires.
func (EKSIdentityExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "ServiceAccount" {
		return nil, nil
	}
	arn := strings.TrimSpace(r.Annotations[irsaAnnotation])
	if arn == "" {
		return nil, nil
	}
	to := externalIdentityID(r.ClusterID, EKSIRSARolePlatform, arn)
	return []graph.Edge{{
		From: r.ID(),
		To:   to,
		Type: graph.EdgeTypeBindsPlatformIdentity,
	}}, nil
}

// externalIdentityID builds the synthetic dangling endpoint id used
// across F-209 extractors. Format mirrors graph.Resource.ID():
//
//	single-cluster: _external/<platformKind>/<externalRef>
//	multi-cluster:  <clusterID>:_external/<platformKind>/<externalRef>
//
// The cluster prefix keeps two clusters referencing the same ARN
// from collapsing into one node (P3-T22 federation requirement).
func externalIdentityID(clusterID, platformKind, externalRef string) string {
	base := ExternalIdentityNamespace + "/" + platformKind + "/" + externalRef
	if clusterID == "" {
		return base
	}
	return clusterID + ":" + base
}
