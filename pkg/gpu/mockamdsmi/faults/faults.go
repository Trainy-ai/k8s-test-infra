// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package faults is the AMD simulator's fault catalog: one typed operation
// per fault in MEP-1001's fault model, each compiled to an override patch
// (state the engine serves until cleared) and/or one-shot actions (a kernel
// log line, deleting the device-plugin socket) the node agent performs.
//
// Each operation names the NodeCondition it exists to drive, so a test can
// assert positively that the condition appeared. A check that degrades to
// UNKNOWN on NOT_SUPPORTED must fail such a test, not pass it.
//
// One fault from the original plan is deliberately missing: a /dev/kfd that
// exists but denies write. It cannot be simulated. The check runs as root,
// which bypasses mode bits, and the simulated node is 1:3, which runc's
// default device allow-list always admits (Wave 0 S3). KFD offers the faults
// that can be.
package faults

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/overrides"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

// AllGPUs targets every GPU through the override document's "all" level.
const AllGPUs = -1

// Fault is one catalog operation.
type Fault interface {
	// Compile resolves GPU indices against p and returns what to apply.
	Compile(p *profile.Profile, now time.Time) (Result, error)
}

// Result is a compiled fault.
type Result struct {
	Patch   *overrides.Doc // merged into the override document
	Clear   []string       // per-GPU patches to drop first (BDFs)
	Reset   bool           // drop the whole document first
	Actions []Action       // one-shot side effects for the node agent
}

// Action kinds.
const (
	ActionKmsg       = "kmsg"        // write Line to /dev/kmsg
	ActionKillPlugin = "kill_plugin" // delete the device-plugin socket
)

// Action is a one-shot side effect. It changes nothing in the engine.
type Action struct {
	Kind string
	Line string // ActionKmsg: the full record, priority prefix included
}

// Apply folds r into doc: the clear/reset step, then the patch.
func Apply(doc *overrides.Doc, r Result) {
	if r.Reset {
		*doc = overrides.Doc{}
	}
	for _, bdf := range r.Clear {
		delete(doc.GPUs, bdf)
	}
	doc.Merge(r.Patch)
}

// gpuPatch returns a patch applying fields to GPU index i, or to all GPUs.
func gpuPatch(p *profile.Profile, i int, fields map[string]any) (*overrides.Doc, error) {
	if i == AllGPUs {
		return &overrides.Doc{All: fields}, nil
	}
	bdf, err := bdfOf(p, i)
	if err != nil {
		return nil, err
	}
	return &overrides.Doc{GPUs: map[string]map[string]any{bdf: fields}}, nil
}

func bdfOf(p *profile.Profile, i int) (string, error) {
	if i < 0 || i >= len(p.GPUs) {
		return "", fmt.Errorf("gpu %d: profile %s has GPUs 0-%d", i, p.Name, len(p.GPUs)-1)
	}
	return p.GPUs[i].BDF, nil
}

func patch(d *overrides.Doc, err error) (Result, error) {
	if err != nil {
		return Result{}, err
	}
	return Result{Patch: d}, nil
}

// ECC sets RAS counters on one block. Drives amd-GpuEcc.
type ECC struct {
	GPU                int // index into the profile, or AllGPUs
	Block              string
	Correctable        *uint64
	Uncorrectable      *uint64
	Deferred           *uint64
	CorrectablePerHour *float64 // grows correctable from now on
}

// Compile implements Fault.
func (f ECC) Compile(p *profile.Profile, now time.Time) (Result, error) {
	if !slices.Contains(profile.ECCBlocks, f.Block) {
		return Result{}, fmt.Errorf("ecc: unknown block %q", f.Block)
	}
	b := map[string]any{"disabled": false}
	setIf(b, "correctable", f.Correctable)
	setIf(b, "uncorrectable", f.Uncorrectable)
	setIf(b, "deferred", f.Deferred)
	if f.CorrectablePerHour != nil {
		b["correctable_per_hour"] = *f.CorrectablePerHour
		b["rate_since"] = now.UTC().Format(time.RFC3339)
	}
	return patch(gpuPatch(p, f.GPU, map[string]any{"ecc": map[string]any{"blocks": map[string]any{f.Block: b}}}))
}

// BadPages replaces the retired-page table with the given counts.
// Drives amd-GpuBadPages.
type BadPages struct {
	GPU                            int
	Retired, Pending, Unreservable int
}

// pageSize is the retirement granularity amdgpu reports on MI300-class parts.
const pageSize = 4096

// Compile implements Fault. Addresses are synthetic, distinct and stable, so
// repeated injections render identical output.
func (f BadPages) Compile(p *profile.Profile, _ time.Time) (Result, error) {
	pages := []any{}
	for status, n := range map[string]int{"reserved": f.Retired, "pending": f.Pending, "unreservable": f.Unreservable} {
		if n < 0 {
			return Result{}, fmt.Errorf("bad-pages: negative %s count", status)
		}
	}
	addr := uint64(0x100000)
	for _, s := range []struct {
		status string
		n      int
	}{{"reserved", f.Retired}, {"pending", f.Pending}, {"unreservable", f.Unreservable}} {
		for range s.n {
			pages = append(pages, map[string]any{"address": addr, "size": pageSize, "status": s.status})
			addr += pageSize
		}
	}
	return patch(gpuPatch(p, f.GPU, map[string]any{"bad_pages": pages}))
}

// PCIe degrades the link or raises replay counts. Drives amd-GpuPcie.
type PCIe struct {
	GPU      int
	Width    *int
	SpeedMTs *int
	Replays  *uint64
}

// Compile implements Fault.
func (f PCIe) Compile(p *profile.Profile, _ time.Time) (Result, error) {
	l := map[string]any{}
	setIf(l, "width", f.Width)
	setIf(l, "speed_mts", f.SpeedMTs)
	setIf(l, "replay_count", f.Replays)
	if len(l) == 0 {
		return Result{}, errors.New("pcie: nothing to set")
	}
	return patch(gpuPatch(p, f.GPU, map[string]any{"pcie": l}))
}

// XGMI takes the link between two GPUs down (both ends, so the library and
// the KFD io_links agree) and/or sets WAFL uncorrectable errors. Links are
// addressed by peer, not by link index: the per-GPU link index is an amd-smi
// presentation detail. Drives amd-Xgmi.
type XGMI struct {
	GPU, Peer       int
	Down            *bool
	WAFLUncorrected *uint64 // the xgmi_wafl RAS block
}

// Compile implements Fault.
func (f XGMI) Compile(p *profile.Profile, now time.Time) (Result, error) {
	a, err := bdfOf(p, f.GPU)
	if err != nil {
		return Result{}, err
	}
	d := &overrides.Doc{GPUs: map[string]map[string]any{}}
	if f.Down != nil {
		b, err := bdfOf(p, f.Peer)
		if err != nil {
			return Result{}, err
		}
		if _, ok := p.GPUs[f.GPU].XGMI.Links[b]; !ok {
			return Result{}, fmt.Errorf("xgmi: %s has no link to %s", a, b)
		}
		for _, end := range [][2]string{{a, b}, {b, a}} {
			d.GPUs[end[0]] = map[string]any{"xgmi": map[string]any{"links": map[string]any{end[1]: map[string]any{"down": *f.Down}}}}
		}
	}
	if f.WAFLUncorrected != nil {
		r, err := ECC{GPU: f.GPU, Block: "xgmi_wafl", Uncorrectable: f.WAFLUncorrected}.Compile(p, now)
		if err != nil {
			return Result{}, err
		}
		d.Merge(r.Patch)
	}
	return Result{Patch: d}, nil
}

// Presence makes a GPU disappear (lost) or fall off the bus. Drives
// amd-GpuCount. The node agent remounts the sysfs overlay for it, since a
// structural change is not visible through in-place writes.
type Presence struct {
	GPU   int
	State string // profile.PresenceLost or profile.PresenceFallenOffBus
}

// Compile implements Fault.
func (f Presence) Compile(p *profile.Profile, _ time.Time) (Result, error) {
	if f.State != profile.PresenceLost && f.State != profile.PresenceFallenOffBus {
		return Result{}, fmt.Errorf("presence: %q is not lost or fallen_off_bus", f.State)
	}
	return patch(gpuPatch(p, f.GPU, map[string]any{"presence": f.State}))
}

// Temperature sets one sensor metric (current by default). Drives
// amd-GpuThermal.
type Temperature struct {
	GPU            int
	Sensor, Metric string
	Celsius        float64
}

// Compile implements Fault.
func (f Temperature) Compile(p *profile.Profile, _ time.Time) (Result, error) {
	metric := f.Metric
	if metric == "" {
		metric = "current"
	}
	if !slices.Contains(profile.TempSensors, f.Sensor) || !slices.Contains(profile.TempMetrics, metric) {
		return Result{}, fmt.Errorf("temp: unknown sensor/metric %s/%s", f.Sensor, metric)
	}
	return patch(gpuPatch(p, f.GPU, map[string]any{"temperatures": map[string]any{f.Sensor: map[string]any{metric: f.Celsius}}}))
}

// Driver changes the amdgpu module's initstate or version. Drives
// amd-GpuDriver.
type Driver struct {
	InitState *string
	Version   *string
}

// Compile implements Fault.
func (f Driver) Compile(_ *profile.Profile, _ time.Time) (Result, error) {
	d := map[string]any{}
	setIf(d, "initstate", f.InitState)
	setIf(d, "version", f.Version)
	if len(d) == 0 {
		return Result{}, errors.New("driver: nothing to set")
	}
	return Result{Patch: &overrides.Doc{Node: map[string]any{"driver": d}}}, nil
}

// KFD changes how /dev/kfd is presented (profile.KFDDevices). Drives amd-Kfd.
type KFD struct {
	Device string
}

// Compile implements Fault.
func (f KFD) Compile(_ *profile.Profile, _ time.Time) (Result, error) {
	if !slices.Contains(profile.KFDDevices, f.Device) {
		return Result{}, fmt.Errorf("kfd: %q is not one of %v (write denial cannot be simulated)", f.Device, profile.KFDDevices)
	}
	return Result{Patch: &overrides.Doc{Node: map[string]any{"kfd": map[string]any{"device": f.Device}}}}, nil
}

// SMI makes library calls block or fail: the "delay --ms" and
// "--after-calls" faults. Drives the SMI-timeout paths (amd-GpuDriver or
// UNKNOWN, by check).
type SMI struct {
	Name  string // key in node.smi_faults; reusing a name replaces that fault
	Fault profile.SMIFault
}

// Compile implements Fault.
func (f SMI) Compile(_ *profile.Profile, _ time.Time) (Result, error) {
	if f.Name == "" {
		return Result{}, errors.New("smi: a name is required")
	}
	return Result{
		Patch: &overrides.Doc{Node: map[string]any{"smi_faults": map[string]any{f.Name: toMap(f.Fault)}}},
	}, nil
}

// KillPlugin deletes the device plugin's kubelet socket. Drives the
// device-plugin condition.
type KillPlugin struct{}

// Compile implements Fault.
func (KillPlugin) Compile(_ *profile.Profile, _ time.Time) (Result, error) {
	return Result{Actions: []Action{{Kind: ActionKillPlugin}}}, nil
}

// Healthy clears every override for one GPU, or everything with AllGPUs.
type Healthy struct {
	GPU int
}

// Compile implements Fault.
func (f Healthy) Compile(p *profile.Profile, _ time.Time) (Result, error) {
	if f.GPU == AllGPUs {
		return Result{Reset: true}, nil
	}
	bdf, err := bdfOf(p, f.GPU)
	if err != nil {
		return Result{}, err
	}
	return Result{Clear: []string{bdf}}, nil
}

func setIf[T any](m map[string]any, key string, v *T) {
	if v != nil {
		m[key] = *v
	}
}

func toMap(f profile.SMIFault) map[string]any {
	m := map[string]any{}
	if f.Library != "" {
		m["library"] = f.Library
	}
	if len(f.Functions) > 0 {
		m["functions"] = slices.Clone(f.Functions)
	}
	if f.Argv != "" {
		m["argv"] = f.Argv
	}
	if f.Status != "" {
		m["status"] = f.Status
	}
	m["delay_ms"] = f.DelayMs
	m["after_calls"] = f.AfterCalls
	return m
}
