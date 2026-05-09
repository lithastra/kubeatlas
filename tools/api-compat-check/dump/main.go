// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Command dump-openapi prints the current build's v1alpha1 OpenAPI
// spec to stdout. Used by CI to capture the fresh spec for
// api-compat-check to compare against api/openapi-v1alpha1.json.
//
//	go run ./tools/api-compat-check/dump > /tmp/current.json
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lithastra/kubeatlas/pkg/aggregator"
	"github.com/lithastra/kubeatlas/pkg/api"
	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

func main() {
	srv := api.New("0.0.0.0:0", memory.New(), aggregator.NewRegistry())
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(srv.OpenAPISpecV1Alpha1()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
