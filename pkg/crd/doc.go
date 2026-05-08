// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

// Package crd watches the cluster's CustomResourceDefinition list and
// runs a dynamic informer for every CRD it discovers. Each CRD's
// resource events are converted into graph.Resource values, fed into
// the Rego engine for edge derivation, and persisted to the
// GraphStore — the same pipeline pkg/discovery's InformerManager runs
// for the built-in GVRs, but on the long tail of cluster-installed
// CRDs.
//
// New CRDs that land at runtime (e.g. `helm install cert-manager`
// after KubeAtlas is up) are picked up live: a top-level CRD informer
// reacts to the metadata add/delete and registers / unregisters the
// per-CRD informer accordingly. KubeAtlas does not require restart
// when a CRD changes.
//
// This package is the runtime counterpart to P2-T11's OpenShift
// detector: detector decides which rule packs to load, this package
// decides which informers to run.
package crd
