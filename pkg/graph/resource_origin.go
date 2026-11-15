// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"encoding/hex"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

// ResourceOrigin is a private last-write receipt, never Kubernetes ownership,
// object existence, complete source history, or an API/serialization field.
// Missing legacy receipts stay unknown. A registration token is unique to one
// collector lifetime; a new collector cannot adopt old data merely by name.
type ResourceOrigin struct {
	ClusterID     string `json:"clusterId"`
	Source        string `json:"source"`
	OwnerName     string `json:"ownerName"`
	OwnerUID      string `json:"ownerUID"`
	Group         string `json:"group"`
	Version       string `json:"version"`
	Resource      string `json:"resource"`
	Kind          string `json:"kind"`
	Namespaced    bool   `json:"namespaced"`
	Registration  string `json:"registration"`
	ObjectUID     string `json:"objectUID,omitempty"`
	ObjectVersion string `json:"objectVersion,omitempty"`
}

func (o ResourceOrigin) Valid() bool {
	if len(o.Registration) != 32 || len(o.OwnerName) > 253 || len(o.OwnerUID) > 256 || len(o.Group) > 253 || len(o.Version) > 63 || len(o.Resource) > 63 || len(o.Kind) > 253 || len(o.ClusterID) > 253 || len(o.ObjectUID) > 256 || len(o.ObjectVersion) > 1024 || strings.ContainsFunc(o.OwnerUID+o.Kind+o.ClusterID+o.ObjectUID+o.ObjectVersion, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	token, err := hex.DecodeString(o.Registration)
	return (o.Source == "crd" || o.Source == "gatekeeper") &&
		len(o.ClusterID) <= 253 && !strings.ContainsAny(o.ClusterID, "/:\x00") &&
		len(validation.IsDNS1123Subdomain(o.OwnerName)) == 0 && o.OwnerUID != "" && len(o.OwnerUID) <= 256 && !strings.ContainsAny(o.OwnerUID, "/:\x00") &&
		len(validation.IsDNS1123Subdomain(o.Group)) == 0 && o.Group != "" &&
		len(validation.IsDNS1035Label(o.Version)) == 0 && len(validation.IsDNS1035Label(o.Resource)) == 0 &&
		o.Kind != "" && o.Kind != "Secret" && len(o.Kind) <= 253 && !strings.ContainsAny(o.Kind, "/:\x00 ") && err == nil && len(token) == 16
}

func (o ResourceOrigin) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: o.Group, Version: o.Version, Resource: o.Resource}
}

func (o ResourceOrigin) Matches(r Resource) bool {
	return o.Valid() && ValidResourceVersion(r) && o.ClusterID == r.ClusterID && o.Kind == r.Kind &&
		o.GVR().GroupVersion().String() == r.GroupVersion && o.Namespaced == (r.Namespace != "") && o.ObjectUID == string(r.UID) && o.ObjectVersion == r.ResourceVersion
}

// SourceRegistration is a bounded, process-local lifecycle observation. It
// qualifies the origin receipt, not whether an individual object still exists.
type SourceRegistration struct {
	Origin ResourceOrigin
	Active bool
}

// OriginQualification never authorizes deletion. Even an active matching
// registration does not prove fresh per-object delivery or healthy watches.
func OriginQualification(r Resource, registrations []SourceRegistration) string {
	if !r.Origin.Matches(r) {
		return "unknown"
	}
	qualification := "unknown"
	found := false
	for _, registration := range registrations {
		o := registration.Origin
		if o.ClusterID != r.ClusterID || o.Source != r.Origin.Source || o.GVR() != r.Origin.GVR() || !o.Valid() {
			continue
		}
		if found {
			return "unknown"
		}
		found = true
		descriptor := r.Origin
		descriptor.ObjectUID, descriptor.ObjectVersion = "", ""
		if o != descriptor {
			qualification = "replaced"
			continue
		}
		if !registration.Active {
			qualification = "retired"
			continue
		}
		qualification = "registered"
	}
	return qualification
}
