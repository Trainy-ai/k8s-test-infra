// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package engine is the single source of AMD GPU state for the simulator.
// Both replacement libraries (libamd_smi.so.26, librocm_smi64.so.1) and the
// node agent's sysfs renderer read it through Query, so amd-smi, rocm-smi,
// the device plugin and konduktor's sysfs checks cannot disagree unless a
// test arranges it.
//
// Query is sized to the Wave 0 call census (census.go), not to all of
// amdsmi.h. Every method is keyed by PCI BDF; the rocm-smi bridge maps its
// device index to a BDF through GPUs().
package engine

import (
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/overrides"
	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/profile"
)

// Errors a Query returns. The bridges map them to library status codes.
var (
	// ErrNotFound: no such GPU, or the GPU is lost.
	ErrNotFound = errors.New("no such GPU")
	// ErrNotSupported: the profile does not declare the value, as a real part
	// does not support it (an absent sensor, a disabled RAS block).
	ErrNotSupported = errors.New("not supported")
	// ErrFallenOffBus: the GPU is still in the PCI tree but cannot be queried.
	ErrFallenOffBus = errors.New("GPU has fallen off the bus")
)

// Engine hands out consistent views of the simulated node.
type Engine interface {
	// Snapshot returns the current state. It is cheap: a library calls it
	// once per API call (the hot path), and it re-resolves overrides only
	// when their generation changes. The returned Query is immutable.
	Snapshot() Query
}

// Query is one immutable view of the node. The methods are the census
// surface; census.go maps every implemented library function onto them.
type Query interface {
	// Generation changes whenever the effective state changes.
	Generation() uint64
	// OverrideError is why the current override document was rejected, or
	// nil. A rejected document leaves the last good state in force.
	OverrideError() error

	// Node is node-level state: software versions, driver, /dev/kfd, host.
	Node() profile.Node
	// GPUs is the enumeration, in amd-smi index order. Lost GPUs are absent;
	// a GPU that fell off the bus stays, and its queries fail.
	GPUs() []string
	// GPU returns one GPU's full state (identity, KFD, DRM, PCIe, power,
	// metrics, bad pages, XGMI). Its maps and slices are shared with the
	// snapshot and must be treated as read-only.
	GPU(bdf string) (profile.GPU, error)
	// ECC returns per-block counts for enabled blocks; rates are applied.
	ECC(bdf string) (map[string]ECCCount, error)
	// ECCTotal sums ECC over enabled blocks (amdsmi_get_gpu_total_ecc_count).
	ECCTotal(bdf string) (ECCCount, error)
	// Link describes the path from src to dst (the amdsmi_topo_* family,
	// and the KFD io_links files).
	Link(src, dst string) (Link, error)
	// Temperature reads one sensor metric, ErrNotSupported when undeclared.
	Temperature(bdf, sensor, metric string) (float64, error)
	// SMIFaults returns the faults matching a call, in name order.
	SMIFaults(library, function, argv string) []profile.SMIFault
}

// ECCCount is one set of RAS counters.
type ECCCount struct {
	Correctable, Uncorrectable, Deferred uint64
}

// Link types, as amdsmi_link_type_t names them.
const (
	LinkXGMI = "xgmi"
	LinkPCIe = "pcie"
)

// PCIe-path weights and hops for GPU pairs without an XGMI link. They follow
// the KFD convention (one PCIe hop to the CPU node weighs 20) and are
// placeholders until a capture of a PCIe-only node replaces them.
const (
	pcieHopWeight   = 20
	crossNUMAWeight = 32
)

// Link is the path between two GPUs.
type Link struct {
	Type       string
	Hops       uint64
	Weight     uint64
	Up         bool // false only for an XGMI link that is down
	Accessible bool // amdsmi_is_P2P_accessible
	MinBWMBps  uint64
	MaxBWMBps  uint64
}

// OverrideSource supplies the override document; *overrides.Store is one.
type OverrideSource interface {
	Snapshot() (gen uint64, doc *overrides.Doc, err error)
}

// Simulator is the in-memory Engine: a profile plus an override source.
type Simulator struct {
	base *profile.Profile
	src  OverrideSource
	now  func() time.Time

	mu      sync.Mutex
	srcGen  uint64
	current *state
	seq     uint64
}

// Option configures a Simulator.
type Option func(*Simulator)

// WithClock sets the clock ECC rates are evaluated against.
func WithClock(now func() time.Time) Option { return func(s *Simulator) { s.now = now } }

// New returns an engine over p. A nil src means no overrides.
func New(p *profile.Profile, src OverrideSource, opts ...Option) *Simulator {
	s := &Simulator{base: p, src: src, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Snapshot implements Engine.
func (s *Simulator) Snapshot() Query {
	s.mu.Lock()
	defer s.mu.Unlock()

	gen, doc, srcErr := uint64(0), (*overrides.Doc)(nil), error(nil)
	if s.src != nil {
		gen, doc, srcErr = s.src.Snapshot()
	}
	if s.current == nil || gen != s.srcGen {
		resolved, err := overrides.Resolve(s.base, doc)
		switch {
		case err == nil:
			s.seq++
			s.current = newState(s.seq, resolved, nil, s.now)
		case s.current == nil:
			s.seq++
			s.current = newState(s.seq, s.base, err, s.now)
		default:
			s.current = s.current.withError(err)
		}
		s.srcGen = gen
	}
	if srcErr != nil {
		return s.current.withError(srcErr)
	}
	return s.current
}

type state struct {
	gen   uint64
	p     *profile.Profile
	err   error
	now   func() time.Time
	index map[string]int
}

func newState(gen uint64, p *profile.Profile, err error, now func() time.Time) *state {
	st := &state{gen: gen, p: p, err: err, now: now, index: map[string]int{}}
	for i, g := range p.GPUs {
		st.index[g.BDF] = i
	}
	return st
}

func (st *state) withError(err error) *state {
	cp := *st
	cp.err = err
	return &cp
}

func (st *state) Generation() uint64   { return st.gen }
func (st *state) OverrideError() error { return st.err }
func (st *state) Node() profile.Node   { return st.p.Node }

func (st *state) GPUs() []string {
	out := make([]string, 0, len(st.p.GPUs))
	for _, g := range st.p.GPUs {
		if g.Presence != profile.PresenceLost {
			out = append(out, g.BDF)
		}
	}
	return out
}

func (st *state) gpu(bdf string) (*profile.GPU, error) {
	i, ok := st.index[strings.ToLower(bdf)]
	if !ok {
		return nil, fmt.Errorf("%s: %w", bdf, ErrNotFound)
	}
	g := &st.p.GPUs[i]
	switch g.Presence {
	case profile.PresenceLost:
		return nil, fmt.Errorf("%s: %w", bdf, ErrNotFound)
	case profile.PresenceFallenOffBus:
		return nil, fmt.Errorf("%s: %w", bdf, ErrFallenOffBus)
	}
	return g, nil
}

func (st *state) GPU(bdf string) (profile.GPU, error) {
	g, err := st.gpu(bdf)
	if err != nil {
		return profile.GPU{}, err
	}
	return *g, nil
}

func (st *state) ECC(bdf string) (map[string]ECCCount, error) {
	g, err := st.gpu(bdf)
	if err != nil {
		return nil, err
	}
	out := map[string]ECCCount{}
	for name, b := range g.ECC.Blocks {
		if b.Disabled {
			continue
		}
		out[name] = ECCCount{Correctable: b.Correctable + st.grown(b), Uncorrectable: b.Uncorrectable, Deferred: b.Deferred}
	}
	return out, nil
}

// grown is how far a correctable rate has advanced the counter. Validation
// guarantees RateSince parses whenever a rate is set.
func (st *state) grown(b profile.ECCBlock) uint64 {
	if b.CorrectablePerHour <= 0 {
		return 0
	}
	since, _ := time.Parse(time.RFC3339, b.RateSince)
	hours := st.now().Sub(since).Hours()
	if hours <= 0 {
		return 0
	}
	return uint64(math.Floor(hours * b.CorrectablePerHour))
}

func (st *state) ECCTotal(bdf string) (ECCCount, error) {
	blocks, err := st.ECC(bdf)
	if err != nil {
		return ECCCount{}, err
	}
	var total ECCCount
	for _, c := range blocks {
		total.Correctable += c.Correctable
		total.Uncorrectable += c.Uncorrectable
		total.Deferred += c.Deferred
	}
	return total, nil
}

func (st *state) Link(src, dst string) (Link, error) {
	a, err := st.gpu(src)
	if err != nil {
		return Link{}, err
	}
	b, err := st.gpu(dst)
	if err != nil {
		return Link{}, err
	}
	if a.BDF == b.BDF {
		return Link{}, fmt.Errorf("link from %s to itself: %w", src, ErrNotSupported)
	}
	if l, ok := a.XGMI.Links[b.BDF]; ok {
		return Link{
			Type: LinkXGMI, Hops: 1, Weight: l.Weight, Up: !l.Down, Accessible: !l.Down,
			MinBWMBps: l.MinBandwidthMBps, MaxBWMBps: l.MaxBandwidthMBps,
		}, nil
	}
	weight := uint64(2 * pcieHopWeight)
	if a.NUMANode != b.NUMANode {
		weight += crossNUMAWeight
	}
	return Link{Type: LinkPCIe, Hops: 2, Weight: weight, Up: true, Accessible: true}, nil
}

func (st *state) Temperature(bdf, sensor, metric string) (float64, error) {
	g, err := st.gpu(bdf)
	if err != nil {
		return 0, err
	}
	v, ok := g.Temps[sensor][metric]
	if !ok {
		return 0, fmt.Errorf("%s temperature %s/%s: %w", bdf, sensor, metric, ErrNotSupported)
	}
	return v, nil
}

func (st *state) SMIFaults(library, function, argv string) []profile.SMIFault {
	var out []profile.SMIFault
	names := make([]string, 0, len(st.p.Node.SMIFaults))
	for name := range st.p.Node.SMIFaults {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f := st.p.Node.SMIFaults[name]
		if f.Library != "" && f.Library != library {
			continue
		}
		if f.Argv != "" && !strings.Contains(argv, f.Argv) {
			continue
		}
		if len(f.Functions) > 0 && !slices.ContainsFunc(f.Functions, func(glob string) bool {
			ok, _ := path.Match(glob, function) // globs are validated on load
			return ok
		}) {
			continue
		}
		out = append(out, f)
	}
	return out
}
