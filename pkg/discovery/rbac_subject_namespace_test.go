// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"fmt"
	"testing"
)

func TestCoreSubjectNamespaceRecoveryAndAuthorization(t *testing.T) {
	for _, fixture := range []struct{ binding, role string }{{"RoleBinding", "Role"}, {"RoleBinding", "ClusterRole"}, {"ClusterRoleBinding", "ClusterRole"}} {
		states := []string{"boolean-namespace", "object-namespace", "write-failure"}
		if fixture.binding == "ClusterRoleBinding" {
			states = append(states, "missing-namespace", "null-namespace", "empty-namespace")
		}
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range states {
					t.Run(fmt.Sprintf("%s/%s/cluster=%s/history=%t/%s", fixture.binding, fixture.role, cluster, history, state), func(t *testing.T) {
						testSubjectReferenceRecovery(t, fixture.binding, fixture.role, cluster, history, state)
					})
				}
			}
		}
	}
}
