// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"fmt"
	"strings"
	"testing"
)

func TestCoreRBACShapeRecoveryAndAuthorization(t *testing.T) {
	for _, fixture := range []struct{ binding, role string }{{"RoleBinding", "Role"}, {"RoleBinding", "ClusterRole"}, {"ClusterRoleBinding", "ClusterRole"}} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"subjects-object", "subjects-string", "subject-null", "subject-array", "roleRef-string", "roleRef-array"} {
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
