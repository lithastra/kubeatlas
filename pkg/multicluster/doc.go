// Package multicluster runs informer pipelines against more than one
// Kubernetes cluster at a time and writes their resources into a
// single GraphStore tagged by ClusterID (P3-T21, F-201).
//
// The package lives at the same level as pkg/discovery — not nested
// underneath it — so the single-cluster informer code stays free of
// multi-cluster awareness. The multicluster.Manager composes the
// existing pkg/discovery.InformerManager rather than re-implementing
// it; each member cluster runs an independent informer with
// discovery.WithClusterID(name) so its events arrive at the store
// already tagged.
//
// The Manager is only constructed when the chart enables it
// (multicluster.enabled=true with a multicluster.kubeconfigSecret).
// In single-cluster mode the original startup path in cmd/kubeatlas
// runs unchanged.
package multicluster
