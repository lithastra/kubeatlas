// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/lithastra/kubeatlas/pkg/crd"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/gatekeeper"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/multicluster"
	"github.com/lithastra/kubeatlas/pkg/operations"
	"github.com/lithastra/kubeatlas/pkg/snapshot"
	kdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

func memberInformerFactory(coreOpts []discovery.InformerOption, tracker *operations.CoverageTracker, rules crd.RegoEvaluator, ext gatekeeper.ExtractorRegistry, metrics *discovery.DynamicMetrics, retained *snapshot.MemberHistoryRetention, writer *snapshot.Writer) multicluster.DiscoveryInformerFactory {
	return func(name string, dyn dynamic.Interface, dc kdiscovery.DiscoveryInterface, store graph.GraphStore) multicluster.InformerStarter {
		repair := discovery.NewMemberRepair(dyn, store, name)
		opts := append(append([]discovery.InformerOption{}, coreOpts...), discovery.WithMemberRepair(repair))
		var recording *snapshot.MemberRecordingSession
		if writer != nil {
			recording = writer.ForMember(name)
			opts = append(opts, discovery.WithSnapshotSink(recording))
		}
		coreFactory := multicluster.DefaultDiscoveryInformerFactory(opts...)
		dynamicManager := discovery.NewDynamicInformerManager(dyn, discovery.WithDynamicMetrics(metrics))
		components := []componentStarter{
			coreFactory(name, dyn, dc, store),
			crd.New(dyn, store, crd.WithCoverage(tracker), crd.WithClusterID(name), crd.WithRegoEvaluator(rules), crd.WithMemberRepair(repair)),
			gatekeeper.New(dyn, store, ext, dynamicManager, gatekeeper.WithCoverage(tracker), gatekeeper.WithClusterID(name), gatekeeper.WithDiscovery(dc), gatekeeper.WithMemberRepair(repair)),
		}
		if retained != nil {
			components = append(components, retained.ForMember(name))
		}
		return &memberCollectors{components: components, recording: recording}
	}
}

// A member owns all of its collectors. Any early exit cancels that member's
// siblings, and Start joins every collector before releasing the member name.
// Other members are owned independently by multicluster.Manager.
type memberCollectors struct {
	components []componentStarter
	recording  *snapshot.MemberRecordingSession
}

func (m *memberCollectors) Start(ctx context.Context) error {
	if len(m.components) == 0 {
		return errors.New("member has no collectors")
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if m.recording != nil {
		if err := m.recording.Begin(child); err != nil {
			return err
		}
		defer m.recording.Close()
	}
	results := make(chan error, len(m.components))
	for _, c := range m.components {
		go func() { results <- c.Start(child) }()
	}
	var first error
	for range m.components {
		err := <-results
		if first == nil && err != nil && !errors.Is(err, context.Canceled) {
			first = err
		}
		cancel()
	}
	if first != nil {
		return fmt.Errorf("member collector: %w", first)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("member collector stopped unexpectedly")
}
