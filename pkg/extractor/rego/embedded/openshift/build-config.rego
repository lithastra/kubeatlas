# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
#
# OpenShift BuildConfig -> ImageStream "BUILDS_TO" edge.
#
# A BuildConfig declares where built images go via spec.output.to.
# Same ImageStreamTag shape as DC's image trigger; same namespace
# fallback rule. v0.1 only models the output target — input image
# (spec.strategy.sourceStrategy.from / dockerStrategy.from) and
# Source Secret references can be added in a future minor.
package kubeatlas.rules.openshift.build_config

import rego.v1

derive contains edge if {
	input.kind == "BuildConfig"
	input.spec.output.to.kind == "ImageStreamTag"
	stream_name := trim_tag(input.spec.output.to.name)
	stream_name != ""
	target_ns := output_namespace
	edge := {
		"type": "BUILDS_TO",
		"from": {
			"kind": "BuildConfig",
			"namespace": input.metadata.namespace,
			"name": input.metadata.name,
		},
		"to": {
			"kind": "ImageStream",
			"namespace": target_ns,
			"name": stream_name,
		},
	}
}

trim_tag(s) := name if {
	idx := indexof(s, ":")
	idx > 0
	name := substring(s, 0, idx)
}

trim_tag(s) := s if {
	indexof(s, ":") == -1
}

# spec.output.to.namespace overrides the BC's own namespace when set.
output_namespace := ns if {
	ns := input.spec.output.to.namespace
	ns != ""
}

output_namespace := input.metadata.namespace if {
	not input.spec.output.to.namespace
}

output_namespace := input.metadata.namespace if {
	input.spec.output.to.namespace == ""
}
