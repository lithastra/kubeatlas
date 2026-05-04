// Package extractor turns Kubernetes resources into typed dependency
// edges in the KubeAtlas graph. Each built-in extractor handles one
// edge type (OWNS, USES_CONFIGMAP, USES_SECRET, MOUNTS_VOLUME, SELECTS,
// USES_SERVICEACCOUNT, ROUTES_TO, ATTACHED_TO).
//
// Extractors are registered through the Extractor interface defined in
// extractor.go and consumed by the informer event handlers in
// pkg/discovery. User-defined extractors written in Rego/Wasm live in
// the sub-package pkg/extractor/rego (enabled in milestone M4).
package extractor
