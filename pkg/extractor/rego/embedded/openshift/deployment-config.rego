# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
#
# OpenShift DeploymentConfig -> ImageStream "TRIGGERS_FROM" edges.
#
# DC is OpenShift's pre-Deployment workload primitive. We do NOT
# emit OWNS for DC -> ReplicationController -> Pod here because the
# built-in OwnerReference extractor in the kubeatlas core already
# covers that — duplicating would mean two edges in the graph for
# the same relationship.
#
# What core can NOT see: DC's spec.triggers[].imageChangeParams.from
# is a soft reference into an ImageStream that triggers a redeploy
# when the stream's tag changes. There's no OwnerReference for this
# link, so it has to come from the rule pack.
#
# imageChangeParams.from has format:
#   { "kind": "ImageStreamTag", "name": "<is>:<tag>", "namespace": "<ns?>" }
#
# We keep the rule conservative: only ImageStreamTag refs (the only
# kind OpenShift currently supports for image triggers); name must
# include the ":<tag>" suffix the API guarantees; namespace defaults
# to the DC's own namespace when absent.
package kubeatlas.rules.openshift.deployment_config

import rego.v1

derive contains edge if {
	input.kind == "DeploymentConfig"
	some trigger in input.spec.triggers
	trigger.type == "ImageChange"
	trigger.imageChangeParams.from.kind == "ImageStreamTag"
	stream_name := trim_tag(trigger.imageChangeParams.from.name)
	stream_name != ""
	target_ns := from_namespace(trigger.imageChangeParams.from)
	edge := {
		"type": "TRIGGERS_FROM",
		"from": {
			"kind": "DeploymentConfig",
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

# trim_tag returns "myimg" for "myimg:latest" / "myimg:v1.2" / etc.
# Empty input returns "" so the caller can skip malformed refs.
trim_tag(s) := name if {
	idx := indexof(s, ":")
	idx > 0
	name := substring(s, 0, idx)
}

trim_tag(s) := s if {
	indexof(s, ":") == -1
}

# from_namespace prefers an explicit namespace on the ref; falls
# back to the DC's own namespace when absent (the OpenShift default).
from_namespace(ref) := ns if {
	ref.namespace != ""
	ns := ref.namespace
}

from_namespace(ref) := input.metadata.namespace if {
	not ref.namespace
}

from_namespace(ref) := input.metadata.namespace if {
	ref.namespace == ""
}
