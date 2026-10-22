// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/lithastra/kubeatlas/pkg/crd"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/gatekeeper"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	kdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

// Standalone keeps the existing three component lifetimes and shared writer.
// runWatch supplies a writer only for explicitly enabled Tier 2 history, joins
// every collector before Stop, and retains its existing global loss accounting.
func standaloneInformerComponents(coreOpts []discovery.InformerOption, dyn dynamic.Interface, dc kdiscovery.DiscoveryInterface, store graph.GraphStore, tracker *operations.CoverageTracker, rules crd.RegoEvaluator, ext gatekeeper.ExtractorRegistry, metrics *discovery.DynamicMetrics, writer *snapshot.Writer) (componentStarter, componentStarter, componentStarter) {
	opts := append(append([]discovery.InformerOption{}, coreOpts...), discovery.WithClusterID(""), discovery.WithAPIInventory(dc))
	var history *discovery.ResourceHistory
	var locks *discovery.MemberRepair
	if writer != nil {
		// Empty-scope MemberRepair provides callback locks only: Run rejects
		// empty scope before any list, ticker or repair work. Standalone missed
		// delete reconciliation is not enabled by this history-only wiring.
		locks = discovery.NewMemberRepair(dyn, store, "")
		history = discovery.NewStandaloneResourceHistory(store, writer)
		opts = append(opts, discovery.WithSnapshotSink(writer), discovery.WithMemberRepair(locks), discovery.WithResourceHistory(history))
	}
	core := discovery.NewInformerManager(dyn, store, opts...)
	custom := crd.New(dyn, store, crd.WithRegoEvaluator(rules), crd.WithCoverage(tracker), crd.WithMemberRepair(locks), crd.WithResourceHistory(history))
	manager := discovery.NewDynamicInformerManager(dyn, discovery.WithDynamicMetrics(metrics))
	constraints := gatekeeper.New(dyn, store, ext, manager, gatekeeper.WithCoverage(tracker), gatekeeper.WithDiscovery(dc), gatekeeper.WithMemberRepair(locks), gatekeeper.WithResourceHistory(history))
	return core, custom, constraints
}
