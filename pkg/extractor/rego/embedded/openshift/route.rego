# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
#
# OpenShift Route -> Service edge derivation.
#
# A Route's spec.to is the canonical pointer at the Service it
# fronts. This rule produces ROUTES_TO edges; weight / TLS / path
# routing are intentionally not modeled here — KubeAtlas's job is
# topology, not request semantics.
package kubeatlas.rules.openshift.route

import rego.v1

derive contains edge if {
	input.kind == "Route"
	input.spec.to.kind == "Service"
	edge := {
		"type": "ROUTES_TO",
		"from": {
			"kind": "Route",
			"namespace": input.metadata.namespace,
			"name": input.metadata.name,
		},
		"to": {
			"kind": "Service",
			"namespace": input.metadata.namespace,
			"name": input.spec.to.name,
		},
	}
}
