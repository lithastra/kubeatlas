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
	repair := discovery.NewStandaloneRepair(dyn, store)
	opts := append(append([]discovery.InformerOption{}, coreOpts...), discovery.WithClusterID(""), discovery.WithAPIInventory(dc), discovery.WithMemberRepair(repair))
	var history *discovery.ResourceHistory
	if writer != nil {
		history = discovery.NewStandaloneResourceHistory(store, writer)
		opts = append(opts, discovery.WithSnapshotSink(writer), discovery.WithResourceHistory(history))
	}
	core := discovery.NewInformerManager(dyn, store, opts...)
	custom := crd.New(dyn, store, crd.WithRegoEvaluator(rules), crd.WithCoverage(tracker), crd.WithMemberRepair(repair), crd.WithResourceHistory(history))
	manager := discovery.NewDynamicInformerManager(dyn, discovery.WithDynamicMetrics(metrics))
	constraints := gatekeeper.New(dyn, store, ext, manager, gatekeeper.WithCoverage(tracker), gatekeeper.WithDiscovery(dc), gatekeeper.WithMemberRepair(repair), gatekeeper.WithResourceHistory(history))
	return core, custom, constraints
}
