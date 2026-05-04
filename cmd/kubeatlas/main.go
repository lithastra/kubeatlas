package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func main() {
	var (
		once      = flag.Bool("once", false, "Run a single discovery pass, write JSON+DOT, and exit (legacy CLI mode).")
		level     = flag.String("level", "resource", "Aggregation level: resource | namespace | cluster.")
		namespace = flag.String("namespace", "", "Filter by namespace (required for level=namespace).")
	)
	flag.Parse()

	if *once {
		runOnce(*level, *namespace)
		return
	}
	runWatch()
}

// runOnce keeps the original CLI behaviour: walk every API resource,
// extract edges, persist into the in-memory store, then write either
// the raw graph (level=resource) or an aggregated view (level=cluster
// or level=namespace) to stdout.
func runOnce(level, namespace string) {
	ctx := context.Background()

	client, err := discovery.NewClient()
	if err != nil {
		log.Fatalf("failed to load kubeconfig: %v", err)
	}
	resources, err := client.CollectAll()
	if err != nil {
		log.Fatalf("failed to collect resources: %v", err)
	}

	store := memory.New()
	for _, r := range resources {
		if err := store.UpsertResource(ctx, r); err != nil {
			log.Fatalf("failed to upsert resource %s: %v", r.ID(), err)
		}
	}

	// Extract edges through the typed extractor.Registry, the same path
	// the informer uses. This gives -once mode full coverage of all
	// eight Phase 0 edge types — the PoC's client.ExtractDependencies
	// only ever emitted a subset (no MOUNTS_VOLUME, no
	// USES_SERVICEACCOUNT). The deprecated PoC method remains exported
	// for one release cycle and is removed in Phase 1 W5.
	reg := extractor.Default()
	for _, r := range resources {
		for _, e := range reg.ExtractAll(r, resources) {
			if err := store.UpsertEdge(ctx, e); err != nil {
				log.Fatalf("failed to upsert edge %s -> %s: %v", e.From, e.To, err)
			}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	switch level {
	case "resource":
		g, err := store.Snapshot(ctx)
		if err != nil {
			log.Fatalf("failed to snapshot store: %v", err)
		}
		if err := enc.Encode(g); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}
		if err := os.WriteFile("output/kubeatlas.dot", []byte(graph.ToDOT(g)), 0644); err != nil {
			log.Fatalf("failed to write DOT file: %v", err)
		}
		fmt.Fprintln(os.Stderr, "Run: dot -Tsvg output/kubeatlas.dot -o output/kubeatlas.svg")

	case "cluster":
		view, err := (aggregator.ClusterAggregator{}).Aggregate(ctx, store, aggregator.Scope{})
		if err != nil {
			log.Fatalf("cluster aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	case "namespace":
		if namespace == "" {
			log.Fatal("-level=namespace requires -namespace=<name>")
		}
		view, err := (aggregator.NamespaceAggregator{}).Aggregate(ctx, store, aggregator.Scope{Namespace: namespace})
		if err != nil {
			log.Fatalf("namespace aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	default:
		log.Fatalf("unknown -level=%q (want resource | namespace | cluster)", level)
	}
}

// runWatch starts a long-lived informer that streams cluster changes
// into the in-memory store. There is no API surface yet, so the
// process simply runs until interrupted; future phases will expose the
// store through pkg/api.
func runWatch() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client, err := discovery.NewClient()
	if err != nil {
		log.Fatalf("failed to load kubeconfig: %v", err)
	}
	gvrs, err := discovery.FilterAvailableGVRs(ctx, discovery.NewDiscoveryFromClient(client), discovery.CoreGVRs)
	if err != nil {
		log.Fatalf("filter GVRs: %v", err)
	}
	store := memory.New()
	mgr := discovery.NewInformerManager(client.Dynamic(), store,
		discovery.WithGVRs(gvrs),
		discovery.WithExtractor(extractor.Default()),
	)
	if err := mgr.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("informer manager: %v", err)
	}
}
