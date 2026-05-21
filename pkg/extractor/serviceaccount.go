package extractor

import (
	"context"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ServiceAccountExtractor emits USES_SERVICEACCOUNT edges from
// workloads (and raw Pods) to the ServiceAccount named by
// spec.template.spec.serviceAccountName. When the field is absent we
// emit the implicit edge to "default" — every Pod runs as some SA, so
// having the edge in the graph keeps the picture honest.
type ServiceAccountExtractor struct{}

func (ServiceAccountExtractor) Type() graph.EdgeType { return graph.EdgeTypeUsesServiceAccount }

func (ServiceAccountExtractor) Extract(_ context.Context, r graph.Resource, _ graph.ResourceLister) ([]graph.Edge, error) {
	if r.Kind != "Pod" && !hasPodTemplate(r.Kind) {
		return nil, nil
	}
	spec := podSpec(r)
	if spec == nil {
		return nil, nil
	}
	name := nestedString(spec, "serviceAccountName")
	if name == "" {
		// Some manifests use the legacy "serviceAccount" field; honour
		// it so we don't miss obvious SA links.
		name = nestedString(spec, "serviceAccount")
	}
	if name == "" {
		name = "default"
	}
	to := graph.Resource{Kind: "ServiceAccount", Name: name, Namespace: r.Namespace, ClusterID: r.ClusterID}.ID()
	return []graph.Edge{{From: r.ID(), To: to, Type: graph.EdgeTypeUsesServiceAccount}}, nil
}
