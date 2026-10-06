// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package overrides_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/overrides"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

const (
	gpu0 = "0000:05:00.0"
	gpu1 = "0000:15:00.0"
)

func loadProfile(t *testing.T) *profile.Profile {
	t.Helper()
	p, err := profile.Load("../configs/mock-amdsmi-config-mi355x.yaml")
	require.NoError(t, err)
	return p
}

func TestResolvePrecedence(t *testing.T) {
	t.Parallel()

	p := loadProfile(t)
	doc, err := overrides.Parse([]byte(`
version: 1
node:
  driver: {initstate: missing}
all:
  ecc: {blocks: {umc: {correctable: 5}}}
gpus:
  "0000:15:00.0":
    ecc: {blocks: {umc: {uncorrectable: 2, correctable: 9}}}
    pcie: {width: 8}
`))
	require.NoError(t, err)

	got, err := overrides.Resolve(p, doc)
	require.NoError(t, err)
	require.Equal(t, "missing", got.Node.Driver.InitState)
	require.Equal(t, "6.14.14", got.Node.Driver.Version, "a node patch merges, it does not replace")

	require.Equal(t, uint64(5), got.GPUs[0].ECC.Blocks["umc"].Correctable, "all applies to every GPU")
	require.Equal(t, uint64(9), got.GPUs[1].ECC.Blocks["umc"].Correctable, "per-GPU wins over all")
	require.Equal(t, uint64(2), got.GPUs[1].ECC.Blocks["umc"].Uncorrectable)
	require.Len(t, got.GPUs[1].ECC.Blocks, 7, "sibling blocks survive the merge")
	require.Equal(t, 8, got.GPUs[1].PCIe.Width)
	require.Equal(t, 16, got.GPUs[1].PCIe.MaxWidth)
	require.Equal(t, 16, got.GPUs[0].PCIe.Width)

	require.Equal(t, uint64(0), p.GPUs[0].ECC.Blocks["umc"].Correctable, "the profile is never modified")
}

func TestResolveRejects(t *testing.T) {
	t.Parallel()

	p := loadProfile(t)
	for name, tc := range map[string]struct{ doc, want string }{
		"unknown gpu":   {`gpus: {"0000:99:00.0": {pcie: {width: 8}}}`, "no such GPU"},
		"unknown field": {`all: {pcie: {widht: 8}}`, `unknown field "widht"`},
		"invalid state": {`all: {pcie: {width: 32}}`, "invalid state"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc, err := overrides.Parse([]byte(tc.doc))
			require.NoError(t, err)
			_, err = overrides.Resolve(p, doc)
			require.ErrorContains(t, err, tc.want)
		})
	}

	_, err := overrides.Parse([]byte("devices: {}"))
	require.ErrorContains(t, err, `unknown field "devices"`, "the NVML mock's index-keyed bucket is not accepted")
}

func TestMergeKeepsExistingFaults(t *testing.T) {
	t.Parallel()

	d := &overrides.Doc{GPUs: map[string]map[string]any{gpu0: {"pcie": map[string]any{"width": 8}}}}
	d.Merge(&overrides.Doc{GPUs: map[string]map[string]any{gpu0: {"pcie": map[string]any{"replay_count": 7}}}})
	require.Equal(t, map[string]any{"width": 8, "replay_count": 7}, d.GPUs[gpu0]["pcie"])
}

func TestStoreGenerations(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "overrides.yaml")
	s := overrides.NewStore(path, 0)

	gen0, doc, err := s.Snapshot()
	require.NoError(t, err)
	require.Empty(t, doc.GPUs, "an absent file is no overrides")

	set := func(width int) {
		require.NoError(t, overrides.Update(path, func(d *overrides.Doc) error {
			d.GPUs = map[string]map[string]any{gpu0: {"pcie": map[string]any{"width": width}}}
			return nil
		}))
	}
	set(8)
	gen1, doc, err := s.Snapshot()
	require.NoError(t, err)
	require.Greater(t, gen1, gen0)
	require.Contains(t, doc.GPUs, gpu0)

	again, _, err := s.Snapshot()
	require.NoError(t, err)
	require.Equal(t, gen1, again, "an unchanged file is not re-read")

	// Same size, likely the same mtime tick: only the inode tells them apart.
	set(4)
	gen2, doc, err := s.Snapshot()
	require.NoError(t, err)
	require.Greater(t, gen2, gen1)
	require.Equal(t, "4", fmt.Sprint(doc.GPUs[gpu0]["pcie"].(map[string]any)["width"]))

	require.NoError(t, os.WriteFile(path, []byte("gpus: [not, a, map"), 0o600))
	gen3, doc, err := s.Snapshot()
	require.Error(t, err)
	require.Equal(t, gen2, gen3, "a broken file does not bump the generation")
	require.Contains(t, doc.GPUs, gpu0, "the last good document is kept")

	require.NoError(t, os.Remove(path))
	gen4, doc, err := s.Snapshot()
	require.NoError(t, err)
	require.Greater(t, gen4, gen3)
	require.Empty(t, doc.GPUs, "removing the file clears every fault")
}

func TestStoreTTL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "overrides.yaml")
	s := overrides.NewStore(path, time.Second)
	gen0, _, err := s.Snapshot()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte("all: {pcie: {width: 8}}"), 0o600))
	gen, _, err := s.Snapshot()
	require.NoError(t, err)
	require.Equal(t, gen0, gen, "within the TTL the file is not even stat'ed")
}

func TestUpdateSerialisesWriters(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "overrides.yaml")
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = overrides.Update(path, func(d *overrides.Doc) error {
				d.Merge(&overrides.Doc{Node: map[string]any{"smi_faults": map[string]any{
					fmt.Sprintf("f%d", i): map[string]any{"delay_ms": i},
				}}})
				return nil
			})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	doc, err := overrides.Parse(data)
	require.NoError(t, err)
	require.Len(t, doc.Node["smi_faults"], 16, "no writer lost another's change")
}
