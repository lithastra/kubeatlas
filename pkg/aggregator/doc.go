// Package aggregator pre-computes graph views at different levels of
// detail (cluster, namespace, workload, resource) so that the API
// layer can return responses without re-walking the full graph on
// every request.
//
// Phase 0 implements the cluster and namespace levels. The workload
// and resource levels are added in Phase 1 W5 alongside the REST API
// in pkg/api.
package aggregator
