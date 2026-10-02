// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package impact consumes and renders captured v1 impact responses. It never
// computes relationships or reads Kubernetes, history payloads, or Secret values.
package impact

import (
	"time"

	"github.com/lithastra/kubeatlas/pkg/graph/analysis"
)

const (
	MaxResponseBytes = 2 << 20
	SharingWarning   = "Contains sensitive cluster topology. Review access and sharing permissions before exporting. Relationships are observations, not a safety or outage guarantee."
)

type Scope struct {
	ClusterID      string `json:"clusterId"`
	Mode           string `json:"mode"`
	NamespaceScope string `json:"namespaceScope"`
}

// Response is the v1-only envelope. It contains allowlisted analysis evidence,
// not Kubernetes objects. Namespace identifies the root, not a traversal filter.
type Response struct {
	SchemaVersion    string                 `json:"schemaVersion"`
	KubeAtlasVersion string                 `json:"kubeatlasVersion"`
	GeneratedAt      time.Time              `json:"generatedAt"`
	Scope            Scope                  `json:"scope"`
	Analysis         *analysis.ImpactResult `json:"analysis"`
	SharingWarning   string                 `json:"sharingWarning"`
}
