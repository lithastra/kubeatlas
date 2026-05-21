// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package multicluster

import (
	"errors"
	"fmt"
	"strings"
)

// ValidateClusterName rejects names the rest of the system cannot
// safely round-trip.
//
// The cluster name becomes the ClusterID on every Resource produced
// by the cluster's informer, and Resource.ID() prefixes it as
// "<clusterID>:<namespace>/<kind>/<name>". Two characters are
// off-limits in the name:
//
//   - the colon ":", which is the ID-prefix separator. A name
//     containing ":" would make a Resource ID ambiguous.
//   - "/", which is the separator between the namespace, kind, and
//     name segments of the rest of the ID.
//
// Empty names are also rejected — every cluster must have a stable
// identifier the federation aggregator and the UI cluster switcher
// can address by.
//
// The check intentionally permits everything else (dashes, dots,
// underscores, mixed case) so operators can name clusters after the
// real-world labels they already use ("prod-eu-west-1", "staging.aks").
func ValidateClusterName(name string) error {
	if name == "" {
		return errors.New("cluster name must not be empty")
	}
	if strings.ContainsAny(name, ":/") {
		return fmt.Errorf("cluster name %q must not contain ':' or '/'", name)
	}
	return nil
}
