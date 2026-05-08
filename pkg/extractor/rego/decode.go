// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package rego

import (
	"fmt"

	"github.com/open-policy-agent/opa/v1/rego"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// decodeEdges turns a Rego v1 result set into graph.Edge values. The
// rule-pack contract (testdata/rules/sample/derive.rego, P2R-T3
// onward) is that the entrypoint returns a SET of edge maps with
// shape:
//
//	{ "type": "STRING",
//	  "from": { "kind", "namespace", "name" },
//	  "to":   { "kind", "namespace", "name" } }
//
// Empty result sets and empty entrypoints are valid (no edges); a
// shape mismatch is an error so a buggy rule is loud rather than
// silently producing zero edges.
func decodeEdges(rs rego.ResultSet) ([]graph.Edge, error) {
	if len(rs) == 0 {
		return nil, nil
	}
	if len(rs[0].Expressions) == 0 {
		return nil, nil
	}

	raw := rs[0].Expressions[0].Value
	if raw == nil {
		return nil, nil
	}

	// rego.v1 sets serialize as []any in Go; some entrypoints
	// (single edge) come through as map[string]any. Accept both.
	switch v := raw.(type) {
	case []any:
		out := make([]graph.Edge, 0, len(v))
		for i, item := range v {
			e, err := decodeEdgeMap(item)
			if err != nil {
				return nil, fmt.Errorf("edge[%d]: %w", i, err)
			}
			out = append(out, e)
		}
		return out, nil
	case map[string]any:
		e, err := decodeEdgeMap(v)
		if err != nil {
			return nil, err
		}
		return []graph.Edge{e}, nil
	default:
		return nil, fmt.Errorf("unexpected rego output type %T (want []any or map[string]any)", raw)
	}
}

// decodeEdgeMap pulls one edge map into a graph.Edge. Type, From,
// To are mandatory; an absent or wrong-typed field is an error.
func decodeEdgeMap(item any) (graph.Edge, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return graph.Edge{}, fmt.Errorf("entry %T is not a map", item)
	}

	typeVal, _ := m["type"].(string)
	if typeVal == "" {
		return graph.Edge{}, fmt.Errorf("missing or non-string \"type\"")
	}

	fromID, err := decodeEndpoint(m["from"], "from")
	if err != nil {
		return graph.Edge{}, err
	}
	toID, err := decodeEndpoint(m["to"], "to")
	if err != nil {
		return graph.Edge{}, err
	}

	return graph.Edge{
		From: fromID,
		To:   toID,
		Type: graph.EdgeType(typeVal),
	}, nil
}

// decodeEndpoint extracts {kind, namespace, name} from one side of
// an edge map and renders it into the canonical resource ID
// "namespace/Kind/name" (graph.Resource.ID() format).
func decodeEndpoint(raw any, side string) (string, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("%s: not a map (got %T)", side, raw)
	}
	kind, _ := m["kind"].(string)
	if kind == "" {
		return "", fmt.Errorf("%s.kind missing or non-string", side)
	}
	namespace, _ := m["namespace"].(string)
	name, _ := m["name"].(string)
	if name == "" {
		return "", fmt.Errorf("%s.name missing or non-string", side)
	}
	return namespace + "/" + kind + "/" + name, nil
}
