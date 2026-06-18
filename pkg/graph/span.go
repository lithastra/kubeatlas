// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import "time"

// Span is one OTLP trace span persisted to the Tier 2 otel_spans
// table (F-204). It is the storage-shaped projection of an OTLP span:
// the receiver translates raw OTLP into this value, the PostgreSQL
// store writes/queries it, and the retention worker prunes it.
//
// The K8s* fields are lifted from the span's resource attributes
// (OTel Semantic Conventions: k8s.namespace.name / k8s.pod.name /
// k8s.deployment.name, service.name) at receive time so the future
// correlator can join spans to graph resources with an indexed
// column lookup rather than a JSONB probe.
//
// Span lives in pkg/graph (alongside ResourceEvent) so both pkg/otel
// and pkg/store/postgres can reference it without the store layer
// having to depend on the otel feature package. It is deliberately
// NOT part of the GraphStore interface: span storage is a Tier 2-only
// concern reached through a narrow seam, so the in-memory backend is
// unaffected.
type Span struct {
	// TraceID and SpanID are the lowercase-hex encodings of the OTLP
	// 16-byte trace id and 8-byte span id. ParentSpanID is the hex
	// parent, or "" for a root span.
	TraceID      string
	SpanID       string
	ParentSpanID string

	// ServiceName is the span's service.name resource attribute.
	ServiceName string

	// K8sNamespace / K8sPod / K8sDeployment are the K8s Semantic-
	// Convention resource attributes, empty when the producer did not
	// set them.
	K8sNamespace  string
	K8sPod        string
	K8sDeployment string

	// StartTime is the span start; DurationNS is end-start in
	// nanoseconds (0 when the producer sent no end time).
	StartTime  time.Time
	DurationNS int64

	// Attributes is the flattened span-level attribute bag (scalar
	// values only), stored as JSONB. nil when the span carried none.
	Attributes map[string]any
}
