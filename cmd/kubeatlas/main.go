package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func main() {
	ctx := context.Background()

	// 1. Connect to the cluster using the default kubeconfig.
	client, err := discovery.NewClient()
	if err != nil {
		log.Fatalf("failed to load kubeconfig: %v", err)
	}

	// 2. Collect all resources.
	resources, err := client.CollectAll()
	if err != nil {
		log.Fatalf("failed to collect resources: %v", err)
	}

	// 3. Extract dependencies.
	edges := client.ExtractDependencies()

	// 4. Persist into the in-memory GraphStore. The CLI path treats the
	//    store as a write-once buffer and immediately reads back through
	//    Snapshot, so future Phase 0 / Phase 1 work that swaps in the
	//    PostgreSQL backend (see pkg/store/postgres) does not need to
	//    touch this entry point.
	store := memory.New()
	for _, r := range resources {
		if err := store.UpsertResource(ctx, r); err != nil {
			log.Fatalf("failed to upsert resource %s: %v", r.ID(), err)
		}
	}
	for _, e := range edges {
		if err := store.UpsertEdge(ctx, e); err != nil {
			log.Fatalf("failed to upsert edge %s -> %s: %v", e.From, e.To, err)
		}
	}
	g, err := store.Snapshot(ctx)
	if err != nil {
		log.Fatalf("failed to snapshot store: %v", err)
	}

	// 5. Write JSON to stdout (2-space indent for human readability).
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(g); err != nil {
		log.Fatalf("failed to encode JSON: %v", err)
	}

	// 6. Write DOT output to a file.
	if err := os.WriteFile("output/kubeatlas.dot", []byte(graph.ToDOT(g)), 0644); err != nil {
		log.Fatalf("failed to write DOT file: %v", err)
	}

	fmt.Fprintln(os.Stderr, "Run: dot -Tsvg output/kubeatlas.dot -o output/kubeatlas.svg")
}
