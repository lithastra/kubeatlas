// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"fmt"
	"strings"
	"testing"
)

func TestCoreRBACIdentityRecoveryAndAuthorization(t *testing.T) {
	for _, fixture := range []struct{ binding, role string }{{"RoleBinding", "Role"}, {"RoleBinding", "ClusterRole"}, {"ClusterRoleBinding", "ClusterRole"}} {
		states := []string{"subject-unsupported-kind", "subject-missing-kind", "subject-malformed-name", "roleRef-unsupported-kind", "roleRef-missing-name", "roleRef-malformed-kind"}
		if fixture.binding == "ClusterRoleBinding" {
			states = append(states, "roleRef-namespaced-role")
		}
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range states {
					t.Run(fmt.Sprintf("%s/%s/cluster=%s/history=%t/%s", fixture.binding, fixture.role, cluster, history, state), func(t *testing.T) {
						if strings.HasPrefix(state, "roleRef-") {
							testRoleRefRecovery(t, fixture.binding, fixture.role, cluster, history, state)
						} else {
							testSubjectReferenceRecovery(t, fixture.binding, fixture.role, cluster, history, state)
						}
					})
				}
			}
		}
	}
}
