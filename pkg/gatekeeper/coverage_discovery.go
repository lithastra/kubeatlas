// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package gatekeeper

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/lithastra/kubeatlas/pkg/operations"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reuses the existing startup/wait-loop discovery call, never an impact-query
// probe. The production client supports contextual discovery; legacy injected
// clients retain their synchronous method (no abandoned cancellation goroutine).
func (d *Discovery) discoverTemplateAPI(ctx context.Context) (operations.SourceDiscoveryState, error) {
	if err := ctx.Err(); err != nil {
		return operations.SourceAPIUnknown, err
	}
	if d.disco == nil {
		return operations.SourceAPIUnknown, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var resources *metav1.APIResourceList
	var err error
	if contextual, ok := d.disco.(interface {
		ServerResourcesForGroupVersionWithContext(context.Context, string) (*metav1.APIResourceList, error)
	}); ok {
		resources, err = contextual.ServerResourcesForGroupVersionWithContext(ctx, constraintTemplateGVR.GroupVersion().String())
	} else {
		resources, err = d.disco.ServerResourcesForGroupVersion(constraintTemplateGVR.GroupVersion().String())
	}
	if ctx.Err() != nil {
		return operations.SourceAPIFailed, ctx.Err()
	}
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return operations.SourceAPINotAdvertised, nil
		case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
			return operations.SourceAPIDenied, err
		default:
			return operations.SourceAPIFailed, err
		}
	}
	if resources == nil || resources.GroupVersion != constraintTemplateGVR.GroupVersion().String() {
		return operations.SourceAPIFailed, errors.New("invalid ConstraintTemplate discovery response")
	}
	for _, resource := range resources.APIResources {
		if resource.Name != constraintTemplateGVR.Resource {
			continue
		}
		if resource.Kind != "ConstraintTemplate" || resource.Namespaced || !slices.Contains(resource.Verbs, "list") || !slices.Contains(resource.Verbs, "watch") {
			return operations.SourceAPIFailed, errors.New("unsupported ConstraintTemplate API shape")
		}
		return operations.SourceAPIAdvertised, nil
	}
	return operations.SourceAPINotAdvertised, nil
}
