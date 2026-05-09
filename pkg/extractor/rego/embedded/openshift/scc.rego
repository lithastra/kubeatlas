# Copyright 2026 The KubeAtlas Authors
# SPDX-License-Identifier: Apache-2.0
#
# OpenShift SecurityContextConstraints "SCC_BINDS" edge to
# ServiceAccount.
#
# SCC is cluster-scoped. Its `users` field is a list of strings
# representing user/SA bindings. ServiceAccount references take the
# form "system:serviceaccount:<namespace>:<saname>" — we parse that
# format and emit one SCC_BINDS edge per SA.
#
# Plain user names (e.g. "alice") and groups (the separate `groups`
# field) are skipped: KubeAtlas doesn't model the IAM identity
# universe outside Kubernetes namespaces, so a "real" user binding
# to an SCC has no graph endpoint we can point at.
package kubeatlas.rules.openshift.scc

import rego.v1

derive contains edge if {
	input.kind == "SecurityContextConstraints"
	some user in input.users
	parts := split(user, ":")
	count(parts) == 4
	parts[0] == "system"
	parts[1] == "serviceaccount"
	ns := parts[2]
	sa := parts[3]
	ns != ""
	sa != ""
	edge := {
		"type": "SCC_BINDS",
		"from": {
			"kind": "SecurityContextConstraints",
			# SCC is cluster-scoped — namespace is empty by
			# convention; the engine accepts that and writes
			# the resource ID as "/SecurityContextConstraints/<n>".
			"namespace": "",
			"name": input.metadata.name,
		},
		"to": {
			"kind": "ServiceAccount",
			"namespace": ns,
			"name": sa,
		},
	}
}
