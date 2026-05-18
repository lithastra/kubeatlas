// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package collect

import (
	"context"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/store/memory"
)

// A non-existent kubeconfig context must surface as an error from
// Cluster rather than a partial or silent result.
func TestCluster_BadContextErrors(t *testing.T) {
	err := Cluster(context.Background(), memory.New(), "", "this-context-does-not-exist")
	if err == nil {
		t.Fatal("Cluster with a bogus context returned a nil error")
	}
}
