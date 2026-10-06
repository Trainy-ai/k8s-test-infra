// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package overrides is the fault-state file the AMD simulator reads at
// runtime: the AMD counterpart of the NVML mock's config overrides
// (pkg/gpu/mocknvml/engine/config_override_*.go), with the same merge
// semantics and the same stat-fingerprint change detection.
//
// The document holds three patch levels, deep-merged in order over the
// profile:
//
//	profile.Node  < node
//	profile.GPUs[i] < all < gpus[<bdf>]
//
// Per-GPU patches are keyed by PCI BDF, not by index. An amd-smi index shifts
// when a GPU is lost; a BDF never does, and it is the key the kernel log, the
// PCI tree and konduktor's per-node assertions already use.
package overrides

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/internal/merge"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

// Version is the only document version this package reads.
const Version = 1

// Doc is the override document. The patches are schema-light maps so any
// profile field is controllable without per-field plumbing; Resolve decodes
// the merged result strictly, so a misspelt key still fails loudly.
type Doc struct {
	Version int                       `json:"version,omitempty"`
	Node    map[string]any            `json:"node,omitempty"`
	All     map[string]any            `json:"all,omitempty"`
	GPUs    map[string]map[string]any `json:"gpus,omitempty"`
}

// Parse strictly decodes an override document. Empty input is no overrides.
func Parse(data []byte) (*Doc, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &Doc{}, nil
	}
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parsing overrides: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber() // keep 64-bit counters exact
	dec.DisallowUnknownFields()
	var d Doc
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("parsing overrides: %w", err)
	}
	if d.Version != 0 && d.Version != Version {
		return nil, fmt.Errorf("parsing overrides: version %d, want %d", d.Version, Version)
	}
	return &d, nil
}

// Bytes renders the document as YAML.
func (d *Doc) Bytes() ([]byte, error) {
	d.Version = Version
	out, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("rendering overrides: %w", err)
	}
	return out, nil
}

// Merge deep-merges patch into d: the way a fault is added to the current
// state without disturbing faults already set.
func (d *Doc) Merge(patch *Doc) {
	if patch == nil {
		return
	}
	d.Node = mergeLevel(d.Node, patch.Node)
	d.All = mergeLevel(d.All, patch.All)
	for bdf, p := range patch.GPUs {
		if d.GPUs == nil {
			d.GPUs = map[string]map[string]any{}
		}
		d.GPUs[bdf] = mergeLevel(d.GPUs[bdf], p)
	}
}

func mergeLevel(dst, src map[string]any) map[string]any {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]any{}
	}
	merge.Into(dst, src)
	return dst
}

// Resolve returns the profile with d applied. It never modifies p, and it
// validates the result exactly as a profile file is validated, so no override
// can produce a state a profile could not declare. A nil d returns a copy of
// p.
func Resolve(p *profile.Profile, d *Doc) (*profile.Profile, error) {
	if d == nil {
		d = &Doc{}
	}
	known := map[string]bool{}
	for _, g := range p.GPUs {
		known[g.BDF] = true
	}
	var errs []error
	for _, bdf := range slices.Sorted(maps.Keys(d.GPUs)) {
		if !known[bdf] {
			errs = append(errs, fmt.Errorf("gpus.%s: no such GPU in profile %s", bdf, p.Name))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	out := *p
	node, err := merge.Apply(&p.Node, d.Node)
	if err != nil {
		return nil, fmt.Errorf("node: %w", err)
	}
	out.Node = *node
	out.GPUs = make([]profile.GPU, len(p.GPUs))
	for i := range p.GPUs {
		g, err := merge.Apply(&p.GPUs[i], d.All, d.GPUs[p.GPUs[i].BDF])
		if err != nil {
			return nil, fmt.Errorf("gpus.%s: %w", p.GPUs[i].BDF, err)
		}
		out.GPUs[i] = *g
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("overrides produce an invalid state: %w", err)
	}
	return &out, nil
}
