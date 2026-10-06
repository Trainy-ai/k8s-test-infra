// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package engine_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/engine"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/overrides"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

const (
	gpu0 = "0000:05:00.0"
	gpu1 = "0000:15:00.0"
	gpu4 = "0000:85:00.0"
)

// source is an OverrideSource whose document a test swaps in.
type source struct {
	gen uint64
	doc *overrides.Doc
	err error
}

func (s *source) Snapshot() (uint64, *overrides.Doc, error) { return s.gen, s.doc, s.err }

func (s *source) set(t *testing.T, yaml string) {
	t.Helper()
	doc, err := overrides.Parse([]byte(yaml))
	require.NoError(t, err)
	s.gen++
	s.doc = doc
}

func newEngine(t *testing.T, opts ...engine.Option) (*engine.Simulator, *source) {
	t.Helper()
	p, err := profile.Load("../configs/mock-amdsmi-config-mi355x.yaml")
	require.NoError(t, err)
	src := &source{}
	return engine.New(p, src, opts...), src
}

func TestCensusSize(t *testing.T) {
	t.Parallel()

	// Wave 0: 25 of 192 amdsmi exports and 13 of 116 rsmi exports carry data.
	require.Len(t, engine.CensusAmdSmi, 25)
	require.Len(t, engine.CensusRocmSmi, 13)
}

func TestPresence(t *testing.T) {
	t.Parallel()

	e, src := newEngine(t)
	require.Len(t, e.Snapshot().GPUs(), 8)

	src.set(t, `gpus: {"0000:05:00.0": {presence: lost}, "0000:15:00.0": {presence: fallen_off_bus}}`)
	q := e.Snapshot()
	require.NoError(t, q.OverrideError())
	require.Len(t, q.GPUs(), 7, "a lost GPU leaves the enumeration")
	require.Equal(t, gpu1, q.GPUs()[0], "one that fell off the bus stays in it")

	_, err := q.GPU(gpu0)
	require.ErrorIs(t, err, engine.ErrNotFound)
	_, err = q.ECCTotal(gpu1)
	require.ErrorIs(t, err, engine.ErrFallenOffBus)
	_, err = q.GPU("0000:99:00.0")
	require.ErrorIs(t, err, engine.ErrNotFound)
}

func TestECC(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	e, src := newEngine(t, engine.WithClock(func() time.Time { return now }))
	src.set(t, `gpus:
  "0000:05:00.0":
    ecc:
      blocks:
        umc: {uncorrectable: 3, correctable: 10, correctable_per_hour: 4, rate_since: "2026-10-06T09:30:00Z"}
        gfx: {correctable: 1}
        sdma: {disabled: true, uncorrectable: 99}
`)
	q := e.Snapshot()
	blocks, err := q.ECC(gpu0)
	require.NoError(t, err)
	require.Equal(t, engine.ECCCount{Correctable: 20, Uncorrectable: 3}, blocks["umc"], "10 + floor(2.5h x 4/h)")
	require.NotContains(t, blocks, "sdma", "a disabled block is not reported")

	total, err := q.ECCTotal(gpu0)
	require.NoError(t, err)
	require.Equal(t, engine.ECCCount{Correctable: 21, Uncorrectable: 3}, total)
}

func TestLink(t *testing.T) {
	t.Parallel()

	e, src := newEngine(t)
	l, err := e.Snapshot().Link(gpu0, gpu1)
	require.NoError(t, err)
	require.Equal(t, engine.Link{Type: engine.LinkXGMI, Hops: 1, Weight: 15, Up: true, Accessible: true, MaxBWMBps: 64000}, l)

	src.set(t, `gpus:
  "0000:05:00.0": {xgmi: {links: {"0000:15:00.0": {down: true}}}}
  "0000:15:00.0": {xgmi: {links: {"0000:05:00.0": {down: true}}}}
`)
	q := e.Snapshot()
	l, err = q.Link(gpu1, gpu0)
	require.NoError(t, err)
	require.False(t, l.Up)
	require.False(t, l.Accessible)
	require.Equal(t, uint64(15), l.Weight, "taking a link down keeps its other attributes")

	_, err = q.Link(gpu0, gpu0)
	require.ErrorIs(t, err, engine.ErrNotSupported)
}

func TestTemperature(t *testing.T) {
	t.Parallel()

	e, src := newEngine(t)
	src.set(t, `gpus: {"0000:85:00.0": {temperatures: {hotspot: {current: 105}}}}`)
	q := e.Snapshot()

	v, err := q.Temperature(gpu4, "hotspot", "current")
	require.NoError(t, err)
	require.InDelta(t, 105.0, v, 0)
	v, err = q.Temperature(gpu4, "hotspot", "critical")
	require.NoError(t, err)
	require.InDelta(t, 110.0, v, 0, "the limit survives a current-temperature fault")

	_, err = q.Temperature(gpu4, "edge", "current")
	require.ErrorIs(t, err, engine.ErrNotSupported)
}

func TestSMIFaults(t *testing.T) {
	t.Parallel()

	e, src := newEngine(t)
	src.set(t, `node:
  smi_faults:
    wedged-ecc: {library: amdsmi, functions: ["amdsmi_get_gpu_*ecc*"], argv: "metric --ecc", delay_ms: 40000}
    flaky: {after_calls: 3, status: busy}
`)
	q := e.Snapshot()

	got := q.SMIFaults(profile.LibraryAmdSmi, "amdsmi_get_gpu_total_ecc_count", "amd-smi metric --ecc --json")
	require.Len(t, got, 2)
	require.Equal(t, 3, got[0].AfterCalls, "faults come back in name order")
	require.Equal(t, 40000, got[1].DelayMs)

	require.Len(t, q.SMIFaults(profile.LibraryAmdSmi, "amdsmi_get_gpu_total_ecc_count", "amd-smi monitor -e"), 1)
	require.Len(t, q.SMIFaults(profile.LibraryRocmSmi, "rsmi_dev_ecc_count_get", "rocm-smi --showrasinfo all"), 1)
}

func TestSnapshotCachingAndRejection(t *testing.T) {
	t.Parallel()

	e, src := newEngine(t)
	first := e.Snapshot()
	require.Same(t, first, e.Snapshot(), "an unchanged generation is served from cache")

	src.set(t, `all: {pcie: {width: 8}}`)
	good := e.Snapshot()
	require.Greater(t, good.Generation(), first.Generation())

	// A document that resolves to an invalid state is refused; the last good
	// state stays in force and the reason is reported.
	src.set(t, `all: {pcie: {width: 64}}`)
	bad := e.Snapshot()
	require.ErrorContains(t, bad.OverrideError(), "invalid state")
	require.Equal(t, good.Generation(), bad.Generation())
	g, err := bad.GPU(gpu0)
	require.NoError(t, err)
	require.Equal(t, 8, g.PCIe.Width)
}
