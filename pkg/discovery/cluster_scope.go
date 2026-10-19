// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"errors"
	"strings"

	"github.com/lithastra/kubeatlas/pkg/graph"
)

// ScopeEdgesToCluster copies rule/extractor edges into the owning member's
// identity space. Already-qualified endpoints must name that same member.
// Colons inside external identity names (after the first slash) are preserved.
// All endpoints are checked before any caller can persist a partial edge set.
func ScopeEdgesToCluster(edges []graph.Edge, cluster string) ([]graph.Edge, error) {
	if cluster == "" {
		return edges, nil
	}
	qualify := func(id string) (string, error) {
		if id == "" {
			return "", errors.New("empty member edge endpoint")
		}
		colon, slash := strings.IndexByte(id, ':'), strings.IndexByte(id, '/')
		if colon >= 0 && (slash < 0 || colon < slash) {
			if id[:colon] != cluster {
				return "", errors.New("edge endpoint outside member cluster")
			}
			return id, nil
		}
		return cluster + ":" + id, nil
	}
	out := make([]graph.Edge, len(edges))
	for i, e := range edges {
		var err error
		if e.From, err = qualify(e.From); err != nil {
			return nil, err
		}
		if e.To, err = qualify(e.To); err != nil {
			return nil, err
		}
		out[i] = e
	}
	return out, nil
}
