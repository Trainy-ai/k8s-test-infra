// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package faults_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/engine"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/faults"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/overrides"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// inject compiles fs in order into one document and returns the engine's
// view of the result: every fault is checked through the same path the
// libraries will read, not by inspecting the patch.
func inject(t *testing.T, fs ...faults.Fault) (engine.Query, []faults.Action) {
	t.Helper()
	p, err := profile.Load("../configs/mock-amdsmi-config-mi355x.yaml")
	require.NoError(t, err)
	doc := &overrides.Doc{}
	var actions []faults.Action
	for _, f := range fs {
		r, err := f.Compile(p, now)
		require.NoError(t, err)
		faults.Apply(doc, r)
		actions = append(actions, r.Actions...)
	}
	q := engine.New(p, static{doc}, engine.WithClock(func() time.Time { return now })).Snapshot()
	require.NoError(t, q.OverrideError())
	return q, actions
}

type static struct{ doc *overrides.Doc }

func (s static) Snapshot() (uint64, *overrides.Doc, error) { return 1, s.doc, nil }

func gpu(t *testing.T, q engine.Query, i int) profile.GPU {
	t.Helper()
	g, err := q.GPU(q.GPUs()[i])
	require.NoError(t, err)
	return g
}

func TestStateFaults(t *testing.T) {
	t.Parallel()

	q, actions := inject(t,
		faults.ECC{GPU: 0, Block: "umc", Uncorrectable: ptr(uint64(2))},
		faults.ECC{GPU: faults.AllGPUs, Block: "gfx", Correctable: ptr(uint64(1))},
		faults.BadPages{GPU: 1, Retired: 2, Pending: 1},
		faults.PCIe{GPU: 2, Width: ptr(8), Replays: ptr(uint64(500))},
		faults.XGMI{GPU: 3, Peer: 4, Down: ptr(true), WAFLUncorrected: ptr(uint64(1))},
		faults.Temperature{GPU: 5, Sensor: "hotspot", Celsius: 105},
		faults.Driver{Version: ptr("6.14.99")},
		faults.KFD{Device: profile.KFDUnopenable},
		faults.SMI{Name: "wedge", Fault: profile.SMIFault{Argv: "metric", DelayMs: 40000}},
	)
	require.Empty(t, actions, "state faults have no side effects")

	total, err := q.ECCTotal(q.GPUs()[0])
	require.NoError(t, err)
	require.Equal(t, engine.ECCCount{Uncorrectable: 2, Correctable: 1}, total)

	pages := gpu(t, q, 1).BadPages
	require.Len(t, pages, 3)
	require.Equal(t, []string{"reserved", "reserved", "pending"}, []string{pages[0].Status, pages[1].Status, pages[2].Status})
	require.NotEqual(t, pages[0].Address, pages[1].Address)

	require.Equal(t, 8, gpu(t, q, 2).PCIe.Width)
	require.Equal(t, uint64(500), gpu(t, q, 2).PCIe.ReplayCount)

	for _, pair := range [][2]int{{3, 4}, {4, 3}} {
		l, err := q.Link(q.GPUs()[pair[0]], q.GPUs()[pair[1]])
		require.NoError(t, err)
		require.False(t, l.Up, "both ends of the link go down")
	}
	wafl, err := q.ECC(q.GPUs()[3])
	require.NoError(t, err)
	require.Equal(t, uint64(1), wafl["xgmi_wafl"].Uncorrectable)

	c, err := q.Temperature(q.GPUs()[5], "hotspot", "current")
	require.NoError(t, err)
	require.InDelta(t, 105.0, c, 0)

	require.Equal(t, "6.14.99", q.Node().Driver.Version)
	require.Equal(t, "live", q.Node().Driver.InitState)
	require.Equal(t, profile.KFDUnopenable, q.Node().KFD.Device)
	require.Len(t, q.SMIFaults(profile.LibraryAmdSmi, "amdsmi_get_temp_metric", "amd-smi metric --temperature"), 1)
}

func TestECCRate(t *testing.T) {
	t.Parallel()

	q, _ := inject(t, faults.ECC{GPU: 0, Block: "umc", CorrectablePerHour: ptr(6.0)})
	blocks, err := q.ECC(q.GPUs()[0])
	require.NoError(t, err)
	require.Equal(t, uint64(0), blocks["umc"].Correctable, "the rate starts counting at injection")
}

func TestPresenceAndHealthy(t *testing.T) {
	t.Parallel()

	q, _ := inject(t, faults.Presence{GPU: 0, State: profile.PresenceLost})
	require.Len(t, q.GPUs(), 7)

	q, _ = inject(t,
		faults.Presence{GPU: 0, State: profile.PresenceLost},
		faults.PCIe{GPU: 1, Width: ptr(4)},
		faults.Healthy{GPU: 0},
	)
	require.Len(t, q.GPUs(), 8, "healthy clears one GPU")
	require.Equal(t, 4, gpu(t, q, 1).PCIe.Width, "and leaves the others alone")

	q, _ = inject(t,
		faults.PCIe{GPU: 1, Width: ptr(4)},
		faults.Driver{InitState: ptr(profile.InitStateMissing)},
		faults.Healthy{GPU: faults.AllGPUs},
	)
	require.Equal(t, 16, gpu(t, q, 1).PCIe.Width)
	require.Equal(t, "live", q.Node().Driver.InitState, "a full reset clears node faults too")
}

// TestKmsgMatchesKonduktorPatterns pins each template to the pattern in
// konduktor's docker/trainy-npd/checks/amd_kernel_errors.py that it exists
// to trip (re.IGNORECASE there, (?i) here).
func TestKmsgMatchesKonduktorPatterns(t *testing.T) {
	t.Parallel()

	for tmpl, pattern := range map[string]string{
		"ring-timeout": `\bring\s+\S+\s+timeout\b`,
		"gpu-reset":    `\bamdgpu\b.*\breset\b`,
		"ras-ue":       `\buncorrectable hardware errors detected in\b.*\bblock\b`,
		"poison":       `\bamdgpu\b.*\b(poison|uncorrectable|fatal)\b`,
		"xgmi-pcs":     `\b(XGMI\w*|WAFL)\s+PCS\s+\w*Err\b`,
	} {
		_, actions := inject(t, faults.Kmsg{GPU: 6, Template: tmpl})
		require.Len(t, actions, 1)
		require.Equal(t, faults.ActionKmsg, actions[0].Kind)
		require.Contains(t, actions[0].Line, "amdgpu 0000:e5:00.0: amdgpu: ", "every line names its GPU")
		require.Regexp(t, `(?i)`+pattern, actions[0].Line, tmpl)
	}
	require.Len(t, faults.KmsgTemplates, 5)
}

func TestCompileRejects(t *testing.T) {
	t.Parallel()

	p, err := profile.Load("../configs/mock-amdsmi-config-mi355x.yaml")
	require.NoError(t, err)
	for name, f := range map[string]faults.Fault{
		"gpu out of range":     faults.PCIe{GPU: 8, Width: ptr(8)},
		"unknown block":        faults.ECC{GPU: 0, Block: "vram"},
		"empty pcie":           faults.PCIe{GPU: 0},
		"unknown template":     faults.Kmsg{GPU: 0, Template: "segfault"},
		"kfd write denial":     faults.KFD{Device: "read_only"},
		"bad presence":         faults.Presence{GPU: 0, State: "asleep"},
		"xgmi to itself":       faults.XGMI{GPU: 0, Peer: 0, Down: ptr(true)},
		"unnamed smi fault":    faults.SMI{},
		"unknown temp sensor":  faults.Temperature{GPU: 0, Sensor: "junction"},
		"negative bad pages":   faults.BadPages{GPU: 0, Pending: -1},
		"healthy out of range": faults.Healthy{GPU: 9},
	} {
		_, err := f.Compile(p, now)
		require.Error(t, err, name)
	}
}
