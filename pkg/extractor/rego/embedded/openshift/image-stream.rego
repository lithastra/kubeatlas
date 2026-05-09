# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
#
# OpenShift ImageStream "TAGGED_FROM" edge.
#
# An ImageStream's spec.tags[].from points at the upstream tag this
# tag tracks. Common patterns: a project-local "latest" tag tracking
# a build output, or a downstream stream tracking the
# openshift/java:openjdk-17-ubi9 base.
#
# The DeploymentConfig rule (deployment-config.rego) already covers
# DC -> IS via image triggers; the BuildConfig rule covers BC -> IS
# output. This rule covers the IS -> IS topology so the graph shows
# the full chain build -> stream-A -> stream-B -> consumer.
#
# We do NOT emit the reverse "IS -> DC" edge requested in early
# P2-T11 sketches: that direction is computed by querying incoming
# edges on the IS node in the graph store, not by emitting a second
# edge from the IS rule (which doesn't have the reverse pointer in
# its spec).
package kubeatlas.rules.openshift.image_stream

import rego.v1

derive contains edge if {
	input.kind == "ImageStream"
	some tag in input.spec.tags
	tag.from.kind == "ImageStreamTag"
	src_name := trim_tag(tag.from.name)
	src_name != ""
	src_ns := tag_from_namespace(tag.from)
	edge := {
		"type": "TAGGED_FROM",
		"from": {
			"kind": "ImageStream",
			"namespace": input.metadata.namespace,
			"name": input.metadata.name,
		},
		"to": {
			"kind": "ImageStream",
			"namespace": src_ns,
			"name": src_name,
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

tag_from_namespace(ref) := ns if {
	ns := ref.namespace
	ns != ""
}

tag_from_namespace(ref) := input.metadata.namespace if {
	not ref.namespace
}

tag_from_namespace(ref) := input.metadata.namespace if {
	ref.namespace == ""
}
