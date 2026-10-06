// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"time"
)

var bdfPattern = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

// problems collects every violation, so one run reports them all.
type problems struct{ errs []error }

// check records a violation when bad holds.
func (p *problems) check(bad bool, format string, args ...any) {
	if bad {
		p.errs = append(p.errs, fmt.Errorf(format, args...))
	}
}

// Validate reports every schema and consistency violation at once. The
// engine runs it on the merged (profile + overrides) state too, so a fault
// can never produce a state the profile itself could not declare.
func (p *Profile) Validate() error {
	var pr problems
	pr.check(p.Version != Version, "version: got %d, want %d", p.Version, Version)
	pr.check(p.Name == "", "name: required")
	pr.check(!slices.Contains([]string{SourceSynthetic, SourceCapture}, p.Source.Kind),
		"source.kind: %q is not synthetic or capture", p.Source.Kind)
	pr.check(len(p.GPUs) == 0, "gpus: at least one GPU is required")
	p.Node.validate(&pr)

	bdfs := map[string]int{}
	for i, g := range p.GPUs {
		_, dup := bdfs[g.BDF]
		pr.check(dup, "gpus[%d].bdf: duplicate %s", i, g.BDF)
		bdfs[g.BDF] = i
	}
	p.validateUnique(&pr)
	for i := range p.GPUs {
		var gp problems
		p.GPUs[i].validate(&gp, bdfs)
		for _, err := range gp.errs {
			pr.check(true, "gpus[%d] (%s): %w", i, p.GPUs[i].BDF, err)
		}
	}
	p.validateLinks(&pr)
	return errors.Join(pr.errs...)
}

// validateUnique rejects two GPUs sharing an identifier the consumers key on.
func (p *Profile) validateUnique(pr *problems) {
	seen := map[string]map[any]int{}
	for i, g := range p.GPUs {
		for field, v := range map[string]any{
			"uuid": g.UUID, "kfd.gpu_id": g.KFD.GPUID, "kfd.node_id": g.KFD.NodeID,
			"drm.card": g.DRM.Card, "drm.render_minor": g.DRM.RenderMinor,
		} {
			if seen[field] == nil {
				seen[field] = map[any]int{}
			}
			j, dup := seen[field][v]
			pr.check(dup, "gpus[%d].%s: %v duplicates gpus[%d]", i, field, v, j)
			seen[field][v] = i
		}
	}
}

// validateLinks checks XGMI link symmetry across GPUs: if a lists b, b lists
// a. A fault takes a link down on both ends, so asymmetry is always an error.
func (p *Profile) validateLinks(pr *problems) {
	byBDF := map[string]*GPU{}
	for i := range p.GPUs {
		byBDF[p.GPUs[i].BDF] = &p.GPUs[i]
	}
	for _, g := range p.GPUs {
		for peer := range g.XGMI.Links {
			if q, ok := byBDF[peer]; ok {
				_, back := q.XGMI.Links[g.BDF]
				pr.check(!back, "xgmi: %s links to %s but not the reverse", g.BDF, peer)
			}
		}
	}
}

func (n *Node) validate(pr *problems) {
	s := n.Software
	pr.check(s.ROCmVersion == "" || s.AmdSmiLibVersion == "" || s.RocmSmiLibVersion == "",
		"node.software: rocm_version, amdsmi_lib_version and rocm_smi_lib_version are required")
	pr.check(!slices.Contains(InitStates, n.Driver.InitState),
		"node.driver.initstate: %q is not one of %v", n.Driver.InitState, InitStates)
	pr.check(n.Driver.Version == "", "node.driver.version: required")
	pr.check(!slices.Contains(KFDDevices, n.KFD.Device), "node.kfd.device: %q is not one of %v", n.KFD.Device, KFDDevices)
	pr.check(n.Host.NUMANodes < 1, "node.host.numa_nodes: must be at least 1")
	for name, f := range n.SMIFaults {
		f.validate(pr, "node.smi_faults."+name)
	}
}

func (f *SMIFault) validate(pr *problems, at string) {
	pr.check(!slices.Contains([]string{"", LibraryAmdSmi, LibraryRocmSmi}, f.Library),
		"%s.library: %q is not amdsmi or rocmsmi", at, f.Library)
	pr.check(!slices.Contains(SMIStatuses, f.Status), "%s.status: %q is not one of %v", at, f.Status, SMIStatuses)
	pr.check(f.DelayMs < 0 || f.AfterCalls < 0, "%s: delay_ms and after_calls must not be negative", at)
	for _, glob := range f.Functions {
		_, err := path.Match(glob, "")
		pr.check(err != nil, "%s.functions: %q: %v", at, glob, err)
	}
}

func (g *GPU) validate(pr *problems, bdfs map[string]int) {
	pr.check(!bdfPattern.MatchString(g.BDF), "bdf: %q is not a lower-case dddd:bb:dd.f address", g.BDF)
	pr.check(g.UUID == "", "uuid: required")
	pr.check(!slices.Contains([]string{"", PresenceLost, PresenceFallenOffBus}, g.Presence),
		"presence: %q is not empty, lost or fallen_off_bus", g.Presence)
	// The device plugin and konduktor's classifier both skip a KFD node whose
	// simd_count is 0: that is how they recognise a CPU node.
	pr.check(g.KFD.GPUID == 0 || g.KFD.SIMDCount == 0, "kfd: gpu_id and simd_count must be non-zero")
	pr.check(g.DRM.Card < 0 || g.DRM.RenderMinor < 128, "drm: card must be >= 0 and render_minor >= 128")
	g.validateECC(pr)
	g.validatePCIe(pr)
	for peer := range g.XGMI.Links {
		_, ok := bdfs[peer]
		pr.check(!ok || peer == g.BDF, "xgmi.links: %q is not another GPU of this profile", peer)
	}
	for i, bp := range g.BadPages {
		pr.check(!slices.Contains(BadPageStatuses, bp.Status),
			"bad_pages[%d].status: %q is not one of %v", i, bp.Status, BadPageStatuses)
	}
	for sensor, metrics := range g.Temps {
		pr.check(!slices.Contains(TempSensors, sensor), "temperatures: unknown sensor %q", sensor)
		for metric := range metrics {
			pr.check(!slices.Contains(TempMetrics, metric), "temperatures.%s: unknown metric %q", sensor, metric)
		}
	}
}

func (g *GPU) validateECC(pr *problems) {
	for block, b := range g.ECC.Blocks {
		pr.check(!slices.Contains(ECCBlocks, block), "ecc.blocks: unknown block %q", block)
		pr.check(b.CorrectablePerHour < 0, "ecc.blocks.%s.correctable_per_hour: must not be negative", block)
		if b.CorrectablePerHour > 0 {
			_, err := time.Parse(time.RFC3339, b.RateSince)
			pr.check(err != nil, "ecc.blocks.%s.rate_since: an RFC 3339 time is required with a rate: %v", block, err)
		}
	}
}

func (g *GPU) validatePCIe(pr *problems) {
	pc := g.PCIe
	pr.check(pc.Width < 0 || pc.MaxWidth <= 0 || pc.Width > pc.MaxWidth,
		"pcie: want 0 <= width <= max_width and max_width > 0, got %d/%d", pc.Width, pc.MaxWidth)
	pr.check(pc.SpeedMTs < 0 || pc.MaxSpeedMTs <= 0 || pc.SpeedMTs > pc.MaxSpeedMTs,
		"pcie: want 0 <= speed_mts <= max_speed_mts and max_speed_mts > 0, got %d/%d", pc.SpeedMTs, pc.MaxSpeedMTs)
}
