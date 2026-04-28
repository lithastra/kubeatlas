package graph

// Resource represents a K8s resource instance.
type Resource struct {
	Kind      string            // e.g. "Deployment"
	Name      string            // e.g. "web-app"
	Namespace string            // e.g. "demo"
	Labels    map[string]string // Used for Service selector matching.
}

// ID returns the resource's unique identifier.
func (r Resource) ID() string {
	return r.Namespace + "/" + r.Kind + "/" + r.Name
}

// Edge represents a dependency between two resources.
type Edge struct {
	From     string // Resource ID
	To       string // Resource ID
	Relation string // e.g. "configMapRef", "secretRef", "selector", "backend"
}

// Graph is the resulting dependency graph.
type Graph struct {
	Resources []Resource
	Edges     []Edge
}
