// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package merge deep-merges schema-light patches over typed values. It has
// the same semantics as the NVML mock's config overrides
// (pkg/gpu/mocknvml/engine/config_override_merge.go): nested maps merge,
// everything else (scalars, lists, null) replaces, and the result is decoded
// strictly so a misspelt key fails instead of being dropped.
package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Into deep-merges src into dst in place.
func Into(dst, src map[string]any) {
	for k, sv := range src {
		if sm, ok := sv.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				Into(dm, sm)
				continue
			}
			sv = Copy(sm)
		}
		dst[k] = sv
	}
}

// Copy deep-copies the map levels of m; leaf values are shared.
func Copy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if vm, ok := v.(map[string]any); ok {
			v = Copy(vm)
		}
		out[k] = v
	}
	return out
}

// ToMap returns the JSON object view of v.
func ToMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshaling: %w", err)
	}
	return Unmarshal(data)
}

// Unmarshal decodes a JSON object, keeping numbers as json.Number. A float64
// round trip would corrupt 64-bit ids (unique_id, hive_id) above 2^53.
func Unmarshal(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	out := map[string]any{}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("unmarshaling to map: %w", err)
	}
	return out, nil
}

// Decode strictly decodes m into out, rejecting unknown fields.
func Decode(m map[string]any, out any) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling merged value: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decoding merged value: %w", err)
	}
	return nil
}

// Apply returns base with each patch deep-merged over it in order, decoded
// into a new T. base is not modified.
func Apply[T any](base *T, patches ...map[string]any) (*T, error) {
	m, err := ToMap(base)
	if err != nil {
		return nil, err
	}
	for _, p := range patches {
		Into(m, p)
	}
	var out T
	if err := Decode(m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
