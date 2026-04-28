package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/lithastra/kubeatlas/pkg/discovery"
	"github.com/lithastra/kubeatlas/pkg/graph"
)

func main() {
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

	// 4. Build the graph.
	g := &graph.Graph{
		Resources: resources,
		Edges:     edges,
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
