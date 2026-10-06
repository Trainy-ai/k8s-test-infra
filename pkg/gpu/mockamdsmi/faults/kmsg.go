// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package faults

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

// Kernel log priorities, as the "<N>" prefix of a /dev/kmsg write.
const (
	kernErr  = 3
	kernInfo = 6
)

type kmsgTemplate struct {
	priority int
	// format takes the block name (for RAS templates) and returns the
	// message after the "amdgpu <bdf>: amdgpu: " prefix dev_err/dev_info add.
	format func(block string) string
}

// KmsgTemplates are amdgpu kernel-log lines, copied from the upstream driver
// format strings (drivers/gpu/drm/amd/amdgpu/, file noted per entry). They
// must also be cross-checked against real dmesg from the fleet before a
// fault golden relies on them. kind nodes share one kernel ring buffer, so
// every line names its GPU's BDF and assertions key on it.
var KmsgTemplates = map[string]kmsgTemplate{
	// amdgpu_job.c, amdgpu_job_timedout()
	"ring-timeout": {kernErr, func(string) string {
		return "ring gfx_0.0.0 timeout, signaled seq=1234, emitted seq=1236"
	}},
	// amdgpu_device.c, amdgpu_device_gpu_recover()
	"gpu-reset": {kernInfo, func(string) string { return "GPU reset(1) succeeded!" }},
	// amdgpu_ras.c, amdgpu_ras_error_print_error_data() (RAS_EVENT_LOG)
	"ras-ue": {kernErr, func(block string) string {
		return fmt.Sprintf("{1}socket: 0, die: 0, 1 new uncorrectable hardware errors detected in %s block", block)
	}},
	// amdgpu_ras.c, amdgpu_ras_do_page_retirement() poison path
	"poison": {kernInfo, func(block string) string {
		return fmt.Sprintf("GPU reset for %s RAS poison consumption is issued!", block)
	}},
	// amdgpu_xgmi.c, __amdgpu_xgmi_report_pcs_error(): "%s detected"
	"xgmi-pcs": {kernInfo, func(string) string { return "XGMI PCS DataLossErr detected" }},
}

// Kmsg writes one amdgpu kernel-log line for a GPU. Drives the
// kernel-error condition (amd_kernel_errors.py).
type Kmsg struct {
	GPU      int
	Template string // a KmsgTemplates key
	Block    string // RAS block for ras-ue/poison; default umc
}

// Compile implements Fault.
func (f Kmsg) Compile(p *profile.Profile, _ time.Time) (Result, error) {
	tmpl, ok := KmsgTemplates[f.Template]
	if !ok {
		return Result{}, fmt.Errorf("kmsg: unknown template %q (have %v)", f.Template, slices.Sorted(maps.Keys(KmsgTemplates)))
	}
	bdf, err := bdfOf(p, f.GPU)
	if err != nil {
		return Result{}, err
	}
	block := f.Block
	if block == "" {
		block = "umc"
	}
	line := fmt.Sprintf("<%d>amdgpu %s: amdgpu: %s", tmpl.priority, bdf, tmpl.format(block))
	return Result{Actions: []Action{{Kind: ActionKmsg, Line: line}}}, nil
}
