// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package rego is the rule-evaluation engine for user-defined edge
// extractors. It loads .rego source modules, prepares them once, and
// evaluates them against per-resource JSON input under a CPU-time
// budget so a runaway rule cannot stall the informer pipeline.
//
// Phase 2 deliberately does NOT introduce a Wasm runtime (guide §2.2,
// anti-pattern #9): the engine talks to Rego via the OPA Go SDK
// directly. The Phase 3+ trigger conditions for revisiting wazero
// are listed in guide §4.4.
//
// INVARIANTS callers depend on:
//
//   - LoadModule prepares the query once; Evaluate is the hot path
//     and never re-parses (anti-pattern: building rego.New per call).
//   - Every Evaluate runs through the timeout + recover guards in
//     sandbox.go (guide §2.8). Without those a `count()` over 5K
//     resources or an OPA-internal panic would take the server down.
//   - rego_api: v1 only (P2-T8); v2 modules are rejected at load
//     time, not silently demoted.
package rego
