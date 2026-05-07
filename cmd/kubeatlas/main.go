package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/extractor"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store"
	"github.com/lithastra/kubeatlas/pkg/store/postgres"
	"github.com/lithastra/kubeatlas/pkg/version"
)

// loadStoreConfig builds a store.Config from the process environment.
// The Helm chart sets KUBEATLAS_BACKEND when persistence.enabled is
// true; the Tier 1 default (empty / "memory") preserves the
// zero-config promise (guide §2.3, anti-pattern #10).
//
// Recognized env vars:
//
//	KUBEATLAS_BACKEND  "memory" (default) | "postgres"
//	PGCONN             full DSN, e.g. postgres://user:pass@host:5432/db?sslmode=disable
//
// Future Phase 2 work (P2-T6 CNPG integration) will refine PGCONN
// into individual fields sourced from a Kubernetes Secret.
func loadStoreConfig() store.Config {
	cfg := store.Config{Backend: store.Backend(os.Getenv("KUBEATLAS_BACKEND"))}
	if cfg.Backend == store.BackendPostgres {
		cfg.Postgres = postgres.Config{DSN: os.Getenv("PGCONN")}
	}
	return cfg
}

func main() {
	var (
		once        = flag.Bool("once", false, "Run a single discovery pass, write JSON+DOT, and exit (legacy CLI mode).")
		level       = flag.String("level", "resource", "Aggregation level: resource | namespace | workload | cluster.")
		namespace   = flag.String("namespace", "", "Filter by namespace (required for namespace/workload, optional for resource).")
		kind        = flag.String("kind", "", "Resource Kind (required for workload, and for resource when scoped to a single object).")
		name        = flag.String("name", "", "Resource name (required for workload, and for resource when scoped to a single object).")
		showVersion = flag.Bool("version", false, "Print build metadata (version, commit, date) and exit.")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("kubeatlas %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return
	}
	if *once {
		runOnce(*level, *namespace, *kind, *name)
		return
	}
	runWatch()
}

// runOnce walks every API resource, extracts edges, persists into the
// in-memory store, then writes one of:
//   - raw full graph (level=resource without kind/name) — legacy default
//   - cluster aggregation (level=cluster)
//   - namespace aggregation (level=namespace, requires -namespace)
//   - workload sub-graph (level=workload, requires -namespace + -kind + -name)
//   - single-resource one-hop view (level=resource with -kind + -name + -namespace)
func runOnce(level, namespace, kind, name string) {
	ctx := context.Background()

	client, err := discovery.NewClient()
	if err != nil {
		log.Fatalf("failed to load kubeconfig: %v", err)
	}
	resources, err := client.CollectAll()
	if err != nil {
		log.Fatalf("failed to collect resources: %v", err)
	}

	graphStore, err := store.New(ctx, loadStoreConfig())
	if err != nil {
		log.Fatalf("failed to construct graph store: %v", err)
	}
	for _, r := range resources {
		if err := graphStore.UpsertResource(ctx, r); err != nil {
			log.Fatalf("failed to upsert resource %s: %v", r.ID(), err)
		}
	}

	// Extract edges through the typed extractor.Registry, the same path
	// the informer uses, so -once mode and the watch loop produce the
	// same eight edge types from the same code.
	reg := extractor.Default()
	for _, r := range resources {
		for _, e := range reg.ExtractAll(r, resources) {
			if err := graphStore.UpsertEdge(ctx, e); err != nil {
				log.Fatalf("failed to upsert edge %s -> %s: %v", e.From, e.To, err)
			}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	switch level {
	case "resource":
		// Two modes: scoped (kind+name+namespace given) → single-resource
		// one-hop view; unscoped → legacy raw graph dump + DOT artefact.
		if kind != "" || name != "" {
			if namespace == "" || kind == "" || name == "" {
				log.Fatal("-level=resource scoped mode requires -namespace, -kind, and -name")
			}
			view, err := (aggregator.ResourceAggregator{}).Aggregate(ctx, graphStore,
				aggregator.Scope{Namespace: namespace, Kind: kind, Name: name})
			if err != nil {
				log.Fatalf("resource aggregator: %v", err)
			}
			if err := enc.Encode(view); err != nil {
				log.Fatalf("failed to encode JSON: %v", err)
			}
			return
		}
		g, err := graphStore.Snapshot(ctx)
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
		view, err := (aggregator.ClusterAggregator{}).Aggregate(ctx, graphStore, aggregator.Scope{})
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
		view, err := (aggregator.NamespaceAggregator{}).Aggregate(ctx, graphStore,
			aggregator.Scope{Namespace: namespace})
		if err != nil {
			log.Fatalf("namespace aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	case "workload":
		if namespace == "" || kind == "" || name == "" {
			log.Fatal("-level=workload requires -namespace, -kind, and -name")
		}
		view, err := (aggregator.WorkloadAggregator{}).Aggregate(ctx, graphStore,
			aggregator.Scope{Namespace: namespace, Kind: kind, Name: name})
		if err != nil {
			log.Fatalf("workload aggregator: %v", err)
		}
		if err := enc.Encode(view); err != nil {
			log.Fatalf("failed to encode JSON: %v", err)
		}

	default:
		log.Fatalf("unknown -level=%q (want resource | namespace | workload | cluster)", level)
	}
}

// runWatch starts the informer and the API server in parallel and
// blocks until either errors or the process receives SIGINT/SIGTERM.
// Both shut down when the parent context is cancelled.
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
	graphStore, err := store.New(ctx, loadStoreConfig())
	if err != nil {
		log.Fatalf("failed to construct graph store: %v", err)
	}
	srv := api.New(api.DefaultAddr, graphStore, aggregator.NewRegistry(), api.WithWebFS(webFS))
	mgr := discovery.NewInformerManager(client.Dynamic(), graphStore,
		discovery.WithGVRs(gvrs),
		discovery.WithExtractor(extractor.Default()),
		discovery.WithOnSynced(srv.Readiness().MarkReady),
		discovery.WithBroadcaster(srv.Hub().BroadcastEvent),
	)

	// Run both components under the same cancellable context. If
	// either returns an error (or the user hits Ctrl-C), cancel the
	// context so the other shuts down too.
	type result struct {
		who string
		err error
	}
	results := make(chan result, 2)
	go func() { results <- result{"informer", mgr.Start(ctx)} }()
	go func() { results <- result{"api", srv.Start(ctx)} }()

	first := <-results
	cancel()
	second := <-results

	for _, r := range []result{first, second} {
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			log.Fatalf("%s: %v", r.who, r.err)
		}
	}
}
