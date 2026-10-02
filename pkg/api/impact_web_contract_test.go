// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Browser export validation must cover the complete DTO, not the display-only
// TypeScript subset. This read-only check works in a normal source checkout.
func TestImpactWebSchemaMatchesAPI(t *testing.T) {
	data, err := os.ReadFile("../../web/src/api/impactSchema.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(impactResponseSchema())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("Web export schema differs from the complete Go API response; update the reviewed schema together with the DTO")
	}
}
