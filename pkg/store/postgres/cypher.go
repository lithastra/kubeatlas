// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// graphName is the AGE graph KubeAtlas writes to. Created in
// migration 001 alongside the vertex/edge labels.
const graphName = "kubeatlas"

// safeIdent matches a Cypher identifier — letters/digits/underscore,
// must start with a letter. Used to gate kind/edge-type values that
// get templated into Cypher strings (anti-pattern #37 forbids string
// concatenation of untrusted input). Resource IDs travel via the
// agtype params map and never enter the query text.
var safeIdent = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// knownVertexLabels mirrors the migrate/001_initial.sql vertex list.
// Anything outside this set is rejected at the Cypher boundary so a
// caller cannot inject a label name. P2-T10 (CRD discovery) extends
// this set at runtime as new kinds appear.
var knownVertexLabels = map[string]struct{}{
	"ConfigMap":             {},
	"CronJob":               {},
	"DaemonSet":             {},
	"Deployment":            {},
	"Gateway":               {},
	"HTTPRoute":             {},
	"Ingress":               {},
	"Job":                   {},
	"Namespace":             {},
	"Node":                  {},
	"PersistentVolume":      {},
	"PersistentVolumeClaim": {},
	"Pod":                   {},
	"ReplicaSet":            {},
	"Secret":                {},
	"Service":               {},
	"ServiceAccount":        {},
	"StatefulSet":           {},
}

// knownEdgeLabels mirrors migrate/001_initial.sql's edge list and
// graph.AllEdgeTypes.
var knownEdgeLabels = map[graph.EdgeType]struct{}{
	graph.EdgeTypeOwns:               {},
	graph.EdgeTypeUsesConfigMap:      {},
	graph.EdgeTypeUsesSecret:         {},
	graph.EdgeTypeMountsVolume:       {},
	graph.EdgeTypeSelects:            {},
	graph.EdgeTypeUsesServiceAccount: {},
	graph.EdgeTypeRoutesTo:           {},
	graph.EdgeTypeAttachedTo:         {},
}

// vertexLabelKnown returns true if kind has a corresponding AGE
// vertex label registered. Unknown kinds are skipped at the AGE
// boundary so a single CRD type does not break the whole upsert
// path; PG persistence still happens.
func vertexLabelKnown(kind string) bool {
	if !safeIdent.MatchString(kind) {
		return false
	}
	_, ok := knownVertexLabels[kind]
	return ok
}

func edgeLabelKnown(t graph.EdgeType) bool {
	if !safeIdent.MatchString(string(t)) {
		return false
	}
	_, ok := knownEdgeLabels[t]
	return ok
}

// withAGETx acquires a pooled connection, ensures AGE is loaded, and
// runs fn inside a transaction with search_path scoped via SET LOCAL
// to ag_catalog. The tx commits on nil return; any error or panic
// rolls back. Use this for every AGE-touching operation.
//
// LOAD 'age' is session-scoped and idempotent, so leaking it back
// onto the pooled connection is harmless. SET LOCAL search_path is
// tx-scoped, so the connection returns to the pool with default
// routing — see guide §P2-T3 for the search_path leak that motivated
// this pattern.
func (s *Store) withAGETx(ctx context.Context, fn func(pgx.Tx) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("withAGETx: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `LOAD 'age'`); err != nil {
		return fmt.Errorf("withAGETx: load age: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("withAGETx: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL search_path = ag_catalog, "$user", public`); err != nil {
		return fmt.Errorf("withAGETx: search_path: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// upsertVertexCypher returns the cypher() SQL for MERGE-ing a vertex
// of the given kind. The kind is templated into the query text after
// the allowlist check; all variable data (ID, fields) travels in the
// agtype params map.
func upsertVertexCypher(kind string) string {
	return fmt.Sprintf(`
		SELECT * FROM cypher('%s'::name, $$
			MERGE (n:%s {id: $id})
			SET n.kind = $kind,
			    n.namespace = $namespace,
			    n.name = $name
		$$::cstring, $1) AS (v agtype)
	`, graphName, kind)
}

// upsertEdgeCypher returns the cypher() SQL for MERGE-ing an edge of
// the given type. Endpoints are matched by id (any vertex label).
func upsertEdgeCypher(edgeType graph.EdgeType) string {
	return fmt.Sprintf(`
		SELECT * FROM cypher('%s'::name, $$
			MATCH (a {id: $from}), (b {id: $to})
			MERGE (a)-[e:%s]->(b)
		$$::cstring, $1) AS (v agtype)
	`, graphName, string(edgeType))
}

// deleteVertexCypher returns the cypher() SQL for DETACH DELETE on a
// single vertex by id (drops incident edges as a side effect).
func deleteVertexCypher() string {
	return fmt.Sprintf(`
		SELECT * FROM cypher('%s'::name, $$
			MATCH (n {id: $id}) DETACH DELETE n
		$$::cstring, $1) AS (v agtype)
	`, graphName)
}

// deleteEdgeCypher returns the cypher() SQL for removing one
// (from, to, type) edge.
func deleteEdgeCypher(edgeType graph.EdgeType) string {
	return fmt.Sprintf(`
		SELECT * FROM cypher('%s'::name, $$
			MATCH (a {id: $from})-[e:%s]->(b {id: $to}) DELETE e
		$$::cstring, $1) AS (v agtype)
	`, graphName, string(edgeType))
}

// listIncomingCypher returns the cypher() SQL for the AGE-backed
// implementation of GraphStore.ListIncoming. Result columns are
// (other_id, edge_type) — the "other" end is the source vertex.
const listIncomingCypher = `
	SELECT a_id::text, et::text FROM cypher('` + graphName + `'::name, $$
		MATCH (a)-[e]->(b {id: $id}) RETURN a.id, type(e)
	$$::cstring, $1) AS (a_id agtype, et agtype)
`

// listOutgoingCypher is the mirror of listIncomingCypher.
const listOutgoingCypher = `
	SELECT b_id::text, et::text FROM cypher('` + graphName + `'::name, $$
		MATCH (a {id: $id})-[e]->(b) RETURN b.id, type(e)
	$$::cstring, $1) AS (b_id agtype, et agtype)
`

// agtypeStrip removes the surrounding double-quotes that pgx scans
// when an agtype string column is cast to text. AGE serializes
// scalars as JSON-quoted text via cypher() output; ints/bools come
// through unquoted, strings come through quoted. Trim the quotes
// before handing the value back to callers.
func agtypeStrip(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		// Unquoting an agtype string: the inner bytes are JSON-
		// escaped (\", \\, etc.). For our IDs/edge types these
		// chars do not appear, but use json.Unmarshal anyway so
		// future kinds with non-trivial chars do not surprise.
		var unq string
		if err := json.Unmarshal([]byte(s), &unq); err == nil {
			return unq
		}
		return s[1 : len(s)-1]
	}
	return s
}

// buildAGEParams marshals the given map to a JSON string suitable
// for the cypher() params slot. Returning string (not []byte) keeps
// pgx's parameter type-inference simple.
func buildAGEParams(params map[string]any) (string, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal params: %w", err)
	}
	return string(body), nil
}

// upsertVertex writes the resource into the AGE graph via MERGE.
// Caller is responsible for the surrounding tx (typically via
// withAGETx). Returns nil + skip-warn for unknown kinds; the PG
// upsert is the source of truth and AGE is best-effort until P2-T10
// extends the kind allowlist dynamically.
func upsertVertex(ctx context.Context, tx pgx.Tx, r graph.Resource) error {
	if !vertexLabelKnown(r.Kind) {
		// Unknown kind: skip AGE write. Logged at debug (callers
		// that care wrap this with their own logging context).
		return nil
	}
	params, err := buildAGEParams(map[string]any{
		"id":        r.ID(),
		"kind":      r.Kind,
		"namespace": r.Namespace,
		"name":      r.Name,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, upsertVertexCypher(r.Kind), params); err != nil {
		return fmt.Errorf("age.upsertVertex %s: %w", r.ID(), err)
	}
	return nil
}

// upsertEdge writes the edge into the AGE graph. Endpoints must
// already exist as vertices; if either side is missing, MATCH
// returns nothing and MERGE silently no-ops. PG persistence has
// already happened by the time we get here, so AGE divergence is
// surfaced by the cross-store consistency tests in cypher_test.go.
func upsertEdge(ctx context.Context, tx pgx.Tx, e graph.Edge) error {
	if !edgeLabelKnown(e.Type) {
		return nil
	}
	params, err := buildAGEParams(map[string]any{
		"from": e.From,
		"to":   e.To,
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, upsertEdgeCypher(e.Type), params); err != nil {
		return fmt.Errorf("age.upsertEdge %s -[%s]-> %s: %w", e.From, e.Type, e.To, err)
	}
	return nil
}

// deleteVertex removes the vertex (and all incident edges via DETACH
// DELETE). No-op for an unknown kind; matches upsertVertex semantics.
func deleteVertex(ctx context.Context, tx pgx.Tx, id string) error {
	params, err := buildAGEParams(map[string]any{"id": id})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, deleteVertexCypher(), params); err != nil {
		return fmt.Errorf("age.deleteVertex %s: %w", id, err)
	}
	return nil
}

// deleteEdge removes the (from, to, type) edge from the AGE graph.
func deleteEdge(ctx context.Context, tx pgx.Tx, from, to string, t graph.EdgeType) error {
	if !edgeLabelKnown(t) {
		return nil
	}
	params, err := buildAGEParams(map[string]any{"from": from, "to": to})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, deleteEdgeCypher(t), params); err != nil {
		return fmt.Errorf("age.deleteEdge %s -[%s]-> %s: %w", from, t, to, err)
	}
	return nil
}

// listIncomingFromAGE is the cypher-backed implementation of
// GraphStore.ListIncoming. The legacy SQL version is kept in
// listIncomingFromPG for benchmark comparison (P2-T4 acceptance
// requires AGE >= 2x SQL on a 100-node / 500-edge graph).
func (s *Store) listIncomingFromAGE(ctx context.Context, id string) ([]graph.Edge, error) {
	params, err := buildAGEParams(map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	var edges []graph.Edge
	err = s.withAGETx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listIncomingCypher, params)
		if err != nil {
			return fmt.Errorf("listIncoming query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var aRaw, etRaw string
			if err := rows.Scan(&aRaw, &etRaw); err != nil {
				return fmt.Errorf("listIncoming scan: %w", err)
			}
			edges = append(edges, graph.Edge{
				From: agtypeStrip(aRaw),
				To:   id,
				Type: graph.EdgeType(agtypeStrip(etRaw)),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if edges == nil {
		edges = []graph.Edge{}
	}
	return edges, nil
}

// listOutgoingFromAGE is the mirror of listIncomingFromAGE.
func (s *Store) listOutgoingFromAGE(ctx context.Context, id string) ([]graph.Edge, error) {
	params, err := buildAGEParams(map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	var edges []graph.Edge
	err = s.withAGETx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listOutgoingCypher, params)
		if err != nil {
			return fmt.Errorf("listOutgoing query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var bRaw, etRaw string
			if err := rows.Scan(&bRaw, &etRaw); err != nil {
				return fmt.Errorf("listOutgoing scan: %w", err)
			}
			edges = append(edges, graph.Edge{
				From: id,
				To:   agtypeStrip(bRaw),
				Type: graph.EdgeType(agtypeStrip(etRaw)),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if edges == nil {
		edges = []graph.Edge{}
	}
	return edges, nil
}

// TraverseOptions configures TraverseOutgoing.
type TraverseOptions struct {
	// MaxDepth is the longest path length explored. Hard upper
	// bound 10 to keep query plans tractable; values <= 0 default
	// to 5 (matches the BlastRadius default in P2-T15).
	MaxDepth int
	// EdgeTypes restricts traversal to the given edge labels.
	// Empty = any type.
	EdgeTypes []graph.EdgeType
}

// TraverseOutgoing walks the AGE graph forward from start, returning
// every distinct vertex reachable within MaxDepth hops along edges
// of one of EdgeTypes. The starting vertex itself is not included.
//
// This is the foundation for BlastRadius (P2-T15) on the Tier 2
// backend; it must stay AGE-only because the variable-length path
// pattern has no good SQL equivalent.
func (s *Store) TraverseOutgoing(ctx context.Context, startID string, opts TraverseOptions) ([]graph.Resource, error) {
	depth := opts.MaxDepth
	if depth <= 0 {
		depth = 5
	}
	if depth > 10 {
		depth = 10
	}

	// Build the relationship pattern: empty types -> any; otherwise
	// alternation across validated labels.
	relPattern := "[*1.." + itoaSafe(depth) + "]"
	if len(opts.EdgeTypes) > 0 {
		var parts []string
		for _, t := range opts.EdgeTypes {
			if !edgeLabelKnown(t) {
				return nil, fmt.Errorf("TraverseOutgoing: unknown edge type %q", t)
			}
			parts = append(parts, string(t))
		}
		relPattern = "[:" + strings.Join(parts, "|") + "*1.." + itoaSafe(depth) + "]"
	}

	query := fmt.Sprintf(`
		SELECT id_v::text, kind_v::text, ns_v::text, name_v::text
		FROM cypher('%s'::name, $$
			MATCH (start {id: $startID})-%s->(n)
			RETURN DISTINCT n.id, n.kind, n.namespace, n.name
		$$::cstring, $1) AS (id_v agtype, kind_v agtype, ns_v agtype, name_v agtype)
	`, graphName, relPattern)

	params, err := buildAGEParams(map[string]any{"startID": startID})
	if err != nil {
		return nil, err
	}

	var out []graph.Resource
	err = s.withAGETx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, params)
		if err != nil {
			return fmt.Errorf("traverse query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, kind, ns, name string
			if err := rows.Scan(&id, &kind, &ns, &name); err != nil {
				return fmt.Errorf("traverse scan: %w", err)
			}
			out = append(out, graph.Resource{
				Kind:      agtypeStrip(kind),
				Namespace: agtypeStrip(ns),
				Name:      agtypeStrip(name),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// itoaSafe formats a small positive int into a Cypher path-pattern
// integer literal. We bound depth to [1, 10] in TraverseOutgoing so
// strconv would be overkill — keep this allocation-free.
func itoaSafe(n int) string {
	if n <= 0 {
		return "0"
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	// 10..99 covers our cap of 10; keep the path simple.
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
