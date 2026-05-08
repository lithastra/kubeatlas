# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0

# Sample derive rule used by the P2-T8 loader tests.
#
# Inputs (Rego v1 API):
#   { "kind": "Foo", "metadata": { "namespace": "demo", "name": "x" } }
#
# Output: a single edge from the input Foo to a Bar in the same
# namespace named "<input-name>-target". The shape mirrors what the
# OpenShift / cert-manager packs produce in P2R-T3+.
package kubeatlas.sample

import rego.v1

derive contains edge if {
	input.kind == "Foo"
	edge := {
		"type": "DERIVED_TO",
		"from": {
			"kind": "Foo",
			"namespace": input.metadata.namespace,
			"name": input.metadata.name,
		},
		"to": {
			"kind": "Bar",
			"namespace": input.metadata.namespace,
			"name": sprintf("%s-target", [input.metadata.name]),
		},
	}
}
