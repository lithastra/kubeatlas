// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package postgres

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lithastra/kubeatlas/pkg/graph"
	"github.com/lithastra/kubeatlas/pkg/graph/storetest"
)

func TestPrivateOriginStorageCodec(t *testing.T) {
	r := storetest.ResourceOriginFixture("east")
	body, err := marshalStorageBlob(r)
	if err != nil || !strings.Contains(string(body), "observationOrigin") {
		t.Fatal("missing durable private receipt", err)
	}
	got, err := unmarshalStorageBlob(body, r.ID())
	if err != nil || got.Origin != r.Origin {
		t.Fatal("private round trip", got.Origin, err)
	}
	public, err := json.Marshal(got)
	if err != nil || strings.Contains(string(public), r.Origin.OwnerUID) || strings.Contains(string(public), r.Origin.Registration) {
		t.Fatal("storage envelope became public", string(public), err)
	}
	for _, malformed := range []string{`null`, `"bad"`, `[]`, `{"ownerUID":{"private":"payload-canary"}}`, `{}`} {
		var blob map[string]json.RawMessage
		if err := json.Unmarshal(body, &blob); err != nil {
			t.Fatal(err)
		}
		blob["observationOrigin"] = json.RawMessage(malformed)
		input, _ := json.Marshal(blob)
		got, err := unmarshalStorageBlob(input, r.ID())
		if err != nil || got.ID() != r.ID() || got.Origin != (graph.ResourceOrigin{}) {
			t.Fatal("invalid private metadata hid/deleted legacy row", malformed, got, err)
		}
	}
	r.Kind = "Secret"
	body, err = marshalStorageBlob(r)
	if err != nil || strings.Contains(string(body), "observationOrigin") || strings.Contains(string(body), r.Origin.OwnerUID) {
		t.Fatal("Secret retained origin", string(body), err)
	}
}
