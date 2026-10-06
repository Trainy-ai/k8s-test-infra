// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/internal/merge"
)

// Load reads, expands and validates a profile file.
func Load(file string) (*Profile, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading profile: %w", err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}

// Parse decodes a profile, expands its shorthands and validates the result.
//
// Two shorthands keep an 8-GPU file readable, and both are resolved here so
// nothing downstream ever sees them:
//
//   - gpu_defaults is deep-merged under every entry of gpus (as the NVML
//     mock's device_defaults is), so an entry states only what differs;
//   - xgmi_full_mesh adds that link between every pair of GPUs that does
//     not already declare one.
//
// Decoding is strict: an unknown key is an error, never a silent zero.
func Parse(data []byte) (*Profile, error) {
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("decoding profile: %w", err)
	}
	doc, err := merge.Unmarshal(js)
	if err != nil {
		return nil, fmt.Errorf("decoding profile: %w", err)
	}
	defaults, _ := doc["gpu_defaults"].(map[string]any)
	delete(doc, "gpu_defaults")
	if gpus, ok := doc["gpus"].([]any); ok {
		for i, g := range gpus {
			entry, ok := g.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("gpus[%d]: not a mapping", i)
			}
			merged := merge.Copy(defaults)
			merge.Into(merged, entry)
			gpus[i] = merged
		}
	}
	var p Profile
	if err := merge.Decode(doc, &p); err != nil {
		return nil, fmt.Errorf("decoding profile: %w", err)
	}
	p.expandMesh()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) expandMesh() {
	if p.XGMIFullMesh == nil {
		return
	}
	for i := range p.GPUs {
		for j := range p.GPUs {
			if i == j {
				continue
			}
			g := &p.GPUs[i]
			if g.XGMI.Links == nil {
				g.XGMI.Links = map[string]XGMILink{}
			}
			if _, ok := g.XGMI.Links[p.GPUs[j].BDF]; !ok {
				g.XGMI.Links[p.GPUs[j].BDF] = *p.XGMIFullMesh
			}
		}
	}
}
