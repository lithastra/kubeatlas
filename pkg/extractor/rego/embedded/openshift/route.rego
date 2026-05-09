# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
#
# OpenShift Route -> Service edge derivation.
#
# A Route's spec.to is the canonical pointer at the Service it
# fronts. spec.alternateBackends carries weighted secondary backends
# the same Route may also forward to. We emit one ROUTES_TO edge per
# Service the Route can reach so KubeAtlas users can see at a glance
# which Services are publicly exposed and through which Route.
#
# weight / TLS termination / path routing are intentionally not
# modeled — KubeAtlas's job is topology, not request semantics.
# A user can drill into spec.tls or spec.path on the Route node
# itself when they need that detail.
#
# This file is mirrored verbatim into the kubeatlas main repo at
# pkg/extractor/rego/embedded/openshift/route.rego — release tooling
# keeps the two paths in sync. Edit either to land both.
package kubeatlas.rules.openshift.route

import rego.v1

# Primary backend: spec.to.{kind,name}. The Service is always in the
# same namespace as the Route — OpenShift Routes do not cross
# namespaces.
derive contains edge if {
	input.kind == "Route"
	input.spec.to.kind == "Service"
	input.spec.to.name != ""
	edge := route_edge(input.spec.to.name)
}

# Alternate backends: spec.alternateBackends is a list of
# {kind, name, weight} entries. We emit a ROUTES_TO edge for each
# Service-kind backend; non-Service kinds (rare; OpenShift accepts
# the field but only Service is supported in 4.x) are skipped.
derive contains edge if {
	input.kind == "Route"
	some backend in input.spec.alternateBackends
	backend.kind == "Service"
	backend.name != ""
	edge := route_edge(backend.name)
}

# route_edge builds the canonical edge map from the route's metadata
# plus the backend service name. Factored so primary + alternate
# backends share one shape and one place to evolve.
route_edge(svcName) := {
	"type": "ROUTES_TO",
	"from": {
		"kind": "Route",
		"namespace": input.metadata.namespace,
		"name": input.metadata.name,
	},
	"to": {
		"kind": "Service",
		"namespace": input.metadata.namespace,
		"name": svcName,
	},
}
