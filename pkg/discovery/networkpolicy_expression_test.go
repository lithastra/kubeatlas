// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"fmt"
	"testing"
)

func TestCoreNetworkPolicyMalformedExpressionRecovery(t *testing.T) {
	for _, cluster := range []string{"east", ""} {
		for _, history := range []bool{false, true} {
			for _, mode := range []string{"selects", "ingress", "egress"} {
				t.Run(fmt.Sprintf("cluster=%s/history=%t/%s", cluster, history, mode), func(t *testing.T) {
					testCoreMalformedSelector(t, cluster, history, mode, true)
				})
			}
		}
	}
}

func TestCoreNetworkPolicyExpressionReplayAndImpact(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "CronJob"} {
		for _, cluster := range []string{"east", ""} {
			for _, history := range []bool{false, true} {
				for _, state := range []string{"missing-metadata", "wrong-template", "masked-match", "write-failure"} {
					t.Run(fmt.Sprintf("%s/cluster=%s/history=%t/%s", kind, cluster, history, state), func(t *testing.T) {
						testSelectorMetadataReplay(t, kind, false, cluster, history, state, true)
					})
				}
			}
		}
	}
}
