// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/url"
	"strings"
)

// normalizeBase trims a trailing slash so the path joins below never
// produce a doubled "//".
func normalizeBase(base string) string {
	return strings.TrimRight(base, "/")
}

// resourceURL deep-links to the KubeAtlas UI resource-detail page.
// The route is /resources/{namespace}/{kind}/{name}; a cluster-scoped
// resource uses the "_" namespace sentinel the UI route already
// understands (an empty path segment is not addressable).
func resourceURL(base, namespace, kind, name string) string {
	ns := namespace
	if ns == "" {
		ns = "_"
	}
	return normalizeBase(base) + "/resources/" +
		url.PathEscape(ns) + "/" + url.PathEscape(kind) + "/" + url.PathEscape(name)
}

// namespaceURL deep-links to the topology page scoped to a namespace.
// The level/namespace query params are the canonical scope encoding;
// older UI builds that ignore them still land on the topology page.
func namespaceURL(base, namespace string) string {
	q := url.Values{"level": {"namespace"}, "namespace": {namespace}}
	return normalizeBase(base) + "/topology?" + q.Encode()
}

// clusterURL deep-links to the cluster-level topology page.
func clusterURL(base string) string {
	return normalizeBase(base) + "/topology?level=cluster"
}
