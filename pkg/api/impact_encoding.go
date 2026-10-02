// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

var errImpactResponseLimit = errors.New("impact response exceeds encoded byte cap")
var errImpactEncoding = errors.New("unsupported impact response shape")
var impactTimeType = reflect.TypeFor[time.Time]()

// Preflight the known DTO, incrementally and with cancellation, BEFORE asking
// encoding/json for a whole response buffer. Only a <= cap response reaches the
// final marshal. Individual strings are raw-length checked before escaping;
// maps/interfaces/custom marshalers are rejected rather than admitting arbitrary
// payloads. Tests compare exact sizes/bytes with encoding/json, including the
// embedded authorization step, nils, omitempty, Unicode, and HTML escaping.
func encodeImpactResponse(ctx context.Context, response ImpactResponse, maxBytes int) ([]byte, error) {
	remaining := maxBytes
	if err := measureImpactJSON(ctx, reflect.ValueOf(response), &remaining); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(response)
	if err != nil {
		return nil, errImpactEncoding
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, errImpactResponseLimit
	}
	return body, nil
}

type impactJSONField struct {
	name      string
	index     []int
	omitEmpty bool
	typ       reflect.Type
}

// The response types have a single anonymous value embedding (ImpactStep).
// Flatten its fields exactly as encoding/json does; reject ambiguous schemas.
func impactJSONFields(t reflect.Type) ([]impactJSONField, error) {
	var fields []impactJSONField
	seen := make(map[string]bool)
	var walk func(reflect.Type, []int) error
	walk = func(t reflect.Type, prefix []int) error {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			index := append(append([]int(nil), prefix...), i)
			if f.Anonymous && tag[0] == "" && f.Type.Kind() == reflect.Struct {
				if err := walk(f.Type, index); err != nil {
					return err
				}
				continue
			}
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			if seen[name] {
				return errImpactEncoding
			}
			seen[name] = true
			omit := len(tag) == 2 && tag[1] == "omitempty"
			if len(tag) > 1 && !omit {
				return errImpactEncoding
			}
			fields = append(fields, impactJSONField{name: name, index: index, omitEmpty: omit, typ: f.Type})
		}
		return nil
	}
	if err := walk(t, nil); err != nil {
		return nil, err
	}
	return fields, nil
}

func impactJSONEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String, reflect.Array, reflect.Slice:
		return v.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint64, reflect.Pointer:
		return v.IsZero()
	default:
		return false // encoding/json does not omit zero-valued structs.
	}
}

func measureImpactJSON(ctx context.Context, v reflect.Value, remaining *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	charge := func(n int) error {
		if n > *remaining {
			return errImpactResponseLimit
		}
		*remaining -= n
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return charge(4)
		}
		return measureImpactJSON(ctx, v.Elem(), remaining)
	}
	if v.Type() == impactTimeType {
		body, err := json.Marshal(v.Interface())
		if err != nil {
			return errImpactEncoding
		}
		return charge(len(body))
	}
	if v.Type().Implements(reflect.TypeFor[json.Marshaler]()) {
		return errImpactEncoding
	}
	switch v.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint64:
		if v.Kind() == reflect.String && v.Len()+2 > *remaining {
			return errImpactResponseLimit
		}
		body, err := json.Marshal(v.Interface())
		if err != nil {
			return errImpactEncoding
		}
		return charge(len(body))
	case reflect.Slice:
		if v.IsNil() {
			return charge(4)
		}
		if err := charge(2); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				if err := charge(1); err != nil {
					return err
				}
			}
			if err := measureImpactJSON(ctx, v.Index(i), remaining); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := charge(2); err != nil {
			return err
		}
		fields, err := impactJSONFields(v.Type())
		if err != nil {
			return err
		}
		first := true
		for _, field := range fields {
			value := v.FieldByIndex(field.index)
			if field.omitEmpty && impactJSONEmpty(value) {
				continue
			}
			if !first {
				if err := charge(1); err != nil {
					return err
				}
			}
			first = false
			key, _ := json.Marshal(field.name)
			if err := charge(len(key) + 1); err != nil {
				return err
			}
			if err := measureImpactJSON(ctx, value, remaining); err != nil {
				return err
			}
		}
		return nil
	default:
		return errImpactEncoding
	}
}
