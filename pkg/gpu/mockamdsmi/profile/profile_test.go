// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package profile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

const exampleProfile = "../configs/mock-amdsmi-config-mi355x.yaml"

func TestExampleProfileLoads(t *testing.T) {
	t.Parallel()

	p, err := profile.Load(exampleProfile)
	require.NoError(t, err)
	require.Equal(t, profile.SourceSynthetic, p.Source.Kind, "the example must stay marked synthetic until a capture replaces it")
	require.Len(t, p.GPUs, 8)

	g := p.GPUs[7]
	require.Equal(t, "0000:f5:00.0", g.BDF)
	// gpu_defaults merged under the entry, without clobbering its own keys.
	require.Equal(t, uint32(0x75a3), g.Identity.DeviceID)
	require.Equal(t, "0xSYNTH0000000007", g.Identity.ASICSerial)
	require.Equal(t, uint32(1024), g.KFD.SIMDCount)
	// 64-bit ids survive the merge exactly (no float64 round trip).
	require.Equal(t, uint64(0xa1b2c3d4e5f60007), g.KFD.UniqueID)
	require.Equal(t, uint64(0xd2a5c3b19f8e7a61), g.XGMI.HiveID)
	// xgmi_full_mesh: 7 peers, never itself.
	require.Len(t, g.XGMI.Links, 7)
	require.NotContains(t, g.XGMI.Links, g.BDF)
	require.Equal(t, uint64(15), g.XGMI.Links["0000:05:00.0"].Weight)
	// No edge sensor: a missing sensor is how NOT_SUPPORTED is expressed.
	require.NotContains(t, g.Temps, "edge")
	require.InDelta(t, 110.0, g.Temps["hotspot"]["critical"], 0)
}

func TestParseRejects(t *testing.T) {
	t.Parallel()

	base, err := os.ReadFile(exampleProfile)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		edit func(m map[string]any)
		want string
	}{
		"unknown key": {
			edit: func(m map[string]any) { m["node"].(map[string]any)["drivr"] = map[string]any{} },
			want: `unknown field "drivr"`,
		},
		"duplicate bdf": {
			edit: func(m map[string]any) { gpu(m, 1)["bdf"] = gpu(m, 0)["bdf"] },
			want: "duplicate 0000:05:00.0",
		},
		"render minor collision": {
			edit: func(m map[string]any) { gpu(m, 1)["drm"] = map[string]any{"card": 99, "render_minor": 136} },
			want: "drm.render_minor: 136 duplicates",
		},
		"unknown ecc block": {
			edit: func(m map[string]any) {
				gpu(m, 0)["ecc"] = map[string]any{"blocks": map[string]any{"vram": map[string]any{}}}
			},
			want: `unknown block "vram"`,
		},
		"unknown temperature sensor": {
			edit: func(m map[string]any) {
				gpu(m, 0)["temperatures"] = map[string]any{"junction": map[string]any{"current": 1}}
			},
			want: `unknown sensor "junction"`,
		},
		"degraded width above max": {
			edit: func(m map[string]any) { gpu(m, 0)["pcie"] = map[string]any{"width": 32} },
			want: "width <= max_width",
		},
		"one-sided xgmi link": {
			edit: func(m map[string]any) {
				delete(m, "xgmi_full_mesh")
				gpu(m, 0)["xgmi"] = map[string]any{"links": map[string]any{"0000:15:00.0": map[string]any{"weight": 15}}}
			},
			want: "0000:05:00.0 links to 0000:15:00.0 but not the reverse",
		},
		"kfd write denial is not a presentation": {
			edit: func(m map[string]any) { m["node"].(map[string]any)["kfd"] = map[string]any{"device": "read_only"} },
			want: `node.kfd.device: "read_only"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var m map[string]any
			require.NoError(t, yaml.Unmarshal(base, &m))
			tc.edit(m)
			data, err := yaml.Marshal(m)
			require.NoError(t, err)

			_, err = profile.Parse(data)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()

	_, err := profile.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	require.ErrorContains(t, err, "reading profile")
}

func gpu(m map[string]any, i int) map[string]any {
	return m["gpus"].([]any)[i].(map[string]any)
}
