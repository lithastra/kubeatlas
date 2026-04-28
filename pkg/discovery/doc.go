// Package discovery connects to a Kubernetes cluster and produces the raw
// material for the dependency graph: a flat list of resources collected via
// the discovery and dynamic clients, plus the edges extracted from their
// specs (owner references, configMap/secret references, Service selectors,
// Ingress backends, and Gateway API parent/backend references).
package discovery
