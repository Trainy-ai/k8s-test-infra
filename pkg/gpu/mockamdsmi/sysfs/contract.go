// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sysfs is the contract between the AMD engine and the node agent
// that presents it as files: which paths the consumers read, where each
// value comes from, and how the files must be written.
//
// Delivery (Wave 0 S2): a non-AMD host has no /sys/module/amdgpu or
// /sys/class/kfd, and runc cannot create a mount point in read-only sysfs, so
// bind mounts do not work. The node agent instead renders a tree under a
// host directory and mounts it as the upper half of an overlay whose lower
// layer is the real sysfs, at each of OverlayMounts, from the node's mount
// namespace (nsenter -t 1 -m) and before the device plugin starts.
//
// That delivery imposes the in-place rule: see WriteInPlace.
package sysfs

import (
	"fmt"
	"os"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/engine"
)

// OverlayMounts are the sysfs directories the node agent overlays.
var OverlayMounts = []string{"/sys/module", "/sys/class", "/sys/bus/pci/devices"}

// Entry is one rendered path, relative to the root of the tree being
// rendered (so "sys/module/amdgpu/initstate", "proc/cpuinfo").
type Entry struct {
	Path    string
	Dir     bool
	Content string // file contents; empty for a directory
}

// Renderer turns one engine snapshot into the complete set of entries. It is
// a pure function of the Query: the node agent diffs successive renders and
// applies the difference (WriteInPlace for changed files, a remount when an
// entry appears or disappears).
type Renderer interface {
	Render(q engine.Query) ([]Entry, error)
}

// Path is one path a consumer reads, the engine value behind it, and who
// reads it. Placeholders: <bdf> PCI address, <n> KFD node, <k> io_link,
// <card>/<minor> DRM indices, <block> RAS block.
type Path struct {
	Pattern string
	Source  string // Query method / profile field
	Readers string
}

// Paths is the authoritative list of what the simulator must present, from
// the Wave 0 census (strace of the real CLIs over stub libraries), the S2
// device-plugin run, and konduktor's docker/trainy-npd/checks/amd_*.py. A
// path absent from this list is not read by any consumer we target.
var Paths = []Path{
	// amdgpu module
	{"sys/module/amdgpu/initstate", "Node.Driver.InitState (absent when missing)", "amd-smi, rocm-smi: exit before any library call unless 'live'"},
	{"sys/module/amdgpu/version", "Node.Driver.Version", "amd_gpu_checks.py; must equal amdsmi_get_gpu_driver_info"},
	{"sys/module/amdgpu/drivers/pci:amdgpu/<bdf>/numa_node", "GPU.NUMANode", "device plugin: mandatory, a GPU without it is skipped"},
	{"sys/module/amdgpu/drivers/pci:amdgpu/<bdf>/current_compute_partition", "GPU.Partition.Compute", "device plugin"},
	{"sys/module/amdgpu/drivers/pci:amdgpu/<bdf>/current_memory_partition", "GPU.Partition.Memory", "device plugin"},
	{"sys/module/amdgpu/drivers/pci:amdgpu/<bdf>/drm/card<card>/", "GPU.DRM.Card", "device plugin"},
	{"sys/module/amdgpu/drivers/pci:amdgpu/<bdf>/drm/renderD<minor>/", "GPU.DRM.RenderMinor", "device plugin"},
	// KFD topology
	{"sys/class/kfd/kfd/topology/generation_id", "Generation", "device plugin"},
	{"sys/class/kfd/kfd/topology/system_properties", "constant", "device plugin"},
	{"sys/class/kfd/kfd/topology/nodes/<n>/properties", "GPU.KFD, GPU.DRM.RenderMinor, BDF (location_id, domain); CPU nodes: Node.Host.NUMANodes", "device plugin (drm_render_minor, unique_id, simd_count); amd_gpu_checks.py (location_id, domain, simd_count > 0)"},
	{"sys/class/kfd/kfd/topology/nodes/<n>/gpu_id", "GPU.KFD.GPUID", "device plugin"},
	{"sys/class/kfd/kfd/topology/nodes/<n>/io_links/<k>/properties", "Link (PCIe type 2 to the CPU node, XGMI type 11 to peers)", "device plugin best-effort allocator; without it p2pWeights init fails"},
	// DRM class
	{"sys/class/drm/card<card>/device/vendor", "GPU.Identity.VendorID", "device plugin"},
	{"sys/class/drm/card<card>/device/device", "GPU.Identity.DeviceID", "device plugin"},
	{"sys/class/drm/card<card>/power/runtime_status", "constant 'active'", "rocm-smi"},
	// PCI functions
	{"sys/bus/pci/devices/<bdf>/vendor", "GPU.Identity.VendorID", "amd_gpu_checks.py; amd-smi (VM-mode passthrough probe)"},
	{"sys/bus/pci/devices/<bdf>/device", "GPU.Identity.DeviceID", "amd_gpu_checks.py; amd-smi"},
	{"sys/bus/pci/devices/<bdf>/class", "GPU.Identity.PCIClass", "amd_gpu_checks.py"},
	{"sys/bus/pci/devices/<bdf>/current_link_width", "GPU.PCIe.Width", "amd_gpu_checks.py (amd-GpuPcie)"},
	{"sys/bus/pci/devices/<bdf>/max_link_width", "GPU.PCIe.MaxWidth", "amd_gpu_checks.py"},
	{"sys/bus/pci/devices/<bdf>/current_link_speed", "GPU.PCIe.SpeedMTs", "capture parity"},
	{"sys/bus/pci/devices/<bdf>/max_link_speed", "GPU.PCIe.MaxSpeedMTs", "capture parity"},
	{"sys/bus/pci/devices/<bdf>/numa_node", "GPU.NUMANode", "capture parity"},
	{"sys/bus/pci/devices/<bdf>/mem_info_vram_total", "GPU.Identity.VRAMBytes", "amd_gpu_checks.py (accelerator vs display)"},
	{"sys/bus/pci/devices/<bdf>/boot_vga", "constant 0", "amd_gpu_checks.py"},
	{"sys/bus/pci/devices/<bdf>/config", "all-ones when Presence is fallen_off_bus", "fault fidelity (amd-GpuCount)"},
	{"sys/bus/pci/devices/<bdf>/ras/<block>_err_count", "ECC", "fault fidelity (amd-GpuEcc); 'ue: N\\nce: N\\n'"},
	{"sys/bus/pci/devices/<bdf>/ras/gpu_vram_bad_pages", "GPU.BadPages", "fault fidelity (amd-GpuBadPages)"},
}

// Bypasses are the non-sysfs files the consumers read directly. The node
// agent stages them; none comes through the libraries.
var Bypasses = []Path{
	{"proc/cpuinfo", "Node.Host.Hypervisor", "amd-smi: the hypervisor flag turns it into a VM build without bad-pages and static --limit"},
	{"dev/kfd", "Node.KFD.Device (c 1:3, absent, regular file, or c 511:0)", "amd_kfd.py opens it O_RDWR; amd-smi stats it when not root"},
	{"dev/dri/card<card>", "GPU.DRM.Card (c 1:3)", "device plugin DeviceSpec; amd-smi when not root"},
	{"dev/dri/renderD<minor>", "GPU.DRM.RenderMinor (c 1:3)", "device plugin DeviceSpec; amd_kfd.py with AMD_KFD_REQUIRE_RENDER"},
	{"opt/rocm/.info/version", "Node.Software.ROCmVersion", "amd-smi version (via librocm-core); amd_gpu_checks.py; ship the real rocm-core package"},
}

// WriteInPlace replaces the contents of an existing file without replacing
// the file (open, truncate, write), creating it only if absent.
//
// Every write into a tree served through the sysfs overlay must use it.
// Wave 0 observed that after an atomic rename in the lower directory the
// overlay kept serving the old, unlinked inode, and later writes to the new
// file stayed invisible until a remount. Never use fsutil.Write (temp file +
// rename) here. New files and directories do appear without a remount, but
// a removed one may not: structural changes (a lost GPU) remount.
func WriteInPlace(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}
