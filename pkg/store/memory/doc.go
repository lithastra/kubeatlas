// Package memory provides the Tier 1 in-memory implementation of the
// graph.GraphStore interface. It is the default store when KubeAtlas
// runs as a single Pod without persistence.
//
// See pkg/store/postgres for the Tier 2 PostgreSQL + Apache AGE
// implementation (enabled in v1.0).
package memory
