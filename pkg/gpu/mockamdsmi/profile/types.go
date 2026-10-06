// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package profile defines the AMD GPU simulator's node profile: the hardware
// and software state of one AMD GPU node, from which the engine answers
// libamd_smi/librocm_smi64 calls and the node agent renders sysfs.
//
// The schema is sized to the Wave 0 call census (MEP-1001), not to all of
// amdsmi.h: every field here is read by at least one amd-smi/rocm-smi
// invocation that konduktor's health checks make, by the device plugin, or by
// a check that reads sysfs directly. Fault injection works by deep-merging
// override patches over this structure (package overrides), so every field
// that a fault changes must live here, including runtime-only ones such as
// GPU.Presence and Node.SMIFaults.
package profile

// Version is the only schema version this package reads.
const Version = 1

// Profile is one simulated AMD GPU node.
type Profile struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Source  Source `json:"source"`
	Node    Node   `json:"node"`
	// GPUs in enumeration order: the order amd-smi assigns logical indices.
	GPUs []GPU `json:"gpus"`
	// XGMIFullMesh, when set, links every GPU pair not linked explicitly
	// (MI300X/MI355X OAM boards are fully connected). Expanded by Parse.
	XGMIFullMesh *XGMILink `json:"xgmi_full_mesh,omitempty"`
}

// Source records where the values came from. A synthetic profile is a
// placeholder; contract tests against golden CLI output need a capture.
type Source struct {
	Kind    string `json:"kind"`              // SourceSynthetic or SourceCapture
	Capture string `json:"capture,omitempty"` // capture tarball name, for SourceCapture
	Note    string `json:"note,omitempty"`
}

// Source kinds.
const (
	SourceSynthetic = "synthetic"
	SourceCapture   = "capture"
)

// Node is state that belongs to the node rather than to one GPU.
type Node struct {
	Software Software `json:"software"`
	Driver   Driver   `json:"driver"`
	KFD      KFD      `json:"kfd"`
	Host     Host     `json:"host"`
	// SMIFaults makes library calls block or fail. Keyed by a caller-chosen
	// name so separate faults merge instead of replacing each other.
	SMIFaults map[string]SMIFault `json:"smi_faults,omitempty"`
}

// Software pins what the simulated node reports as installed.
type Software struct {
	ROCmVersion       string `json:"rocm_version"`         // /opt/rocm/.info/version
	AmdSmiLibVersion  string `json:"amdsmi_lib_version"`   // amdsmi_get_lib_version
	RocmSmiLibVersion string `json:"rocm_smi_lib_version"` // rsmi_version_get
}

// Driver is the amdgpu kernel module as amd-smi, rocm-smi and the checks see
// it. Version is also what amdsmi_get_gpu_driver_info reports, so the
// library and /sys/module/amdgpu/version cannot disagree.
type Driver struct {
	InitState string `json:"initstate"` // one of InitStates; amd-smi exits before any call unless "live"
	Version   string `json:"version"`
}

// InitStates lists /sys/module/amdgpu/initstate values. InitStateMissing
// means the file is absent.
var InitStates = []string{"live", "coming", "going", InitStateMissing}

// InitStateMissing removes /sys/module/amdgpu/initstate.
const InitStateMissing = "missing"

// KFD is the /dev/kfd node.
type KFD struct {
	Device string `json:"device"` // one of KFDDevices
}

// /dev/kfd presentations. A write-denied node is deliberately absent: root
// bypasses mode bits, and a 1:3 node is in runc's default device allow-list,
// so neither mode bits nor the device cgroup can make the open fail (Wave 0
// S3). KFDUnopenable is a char device with an unregistered major, which
// fails with ENXIO.
const (
	KFDCharDevice  = "char_device"  // mknod c 1 3: S_ISCHR, opens read-write
	KFDMissing     = "missing"      // no node
	KFDRegularFile = "regular_file" // a stale regular file
	KFDUnopenable  = "unopenable"   // mknod c 511 0: open fails with ENXIO
)

// KFDDevices lists the valid KFD.Device values.
var KFDDevices = []string{KFDCharDevice, KFDMissing, KFDRegularFile, KFDUnopenable}

// Host is how the simulator presents the host itself.
type Host struct {
	// Hypervisor controls the "hypervisor" flag in the staged /proc/cpuinfo.
	// amd-smi treats a host with the flag as a VM and drops bad-pages and
	// static --limit, so the simulator presents bare metal by default even
	// on a cloud VM.
	Hypervisor bool `json:"hypervisor"`
	NUMANodes  int  `json:"numa_nodes"` // CPU nodes in the KFD topology
}

// SMIFault makes matching library calls slow or failing, modelling a wedged
// management stack. A call matches when Library, every Functions glob (any
// one of them) and Argv all match; empty fields match everything.
type SMIFault struct {
	Library   string   `json:"library,omitempty"`   // "amdsmi" or "rocmsmi"; empty = both
	Functions []string `json:"functions,omitempty"` // path.Match globs, e.g. "amdsmi_get_gpu_*"
	// Argv is a substring of the calling process's command line, so a fault
	// can target one CLI subcommand ("metric --ecc") rather than a function.
	Argv    string `json:"argv,omitempty"`
	DelayMs int    `json:"delay_ms,omitempty"`
	// AfterCalls lets that many matching calls through first. Counted per
	// process: each CLI invocation is a new process and starts at zero.
	AfterCalls int `json:"after_calls,omitempty"`
	// Status is the status returned once the fault fires, by name (one of
	// SMIStatuses). Empty means succeed after the delay.
	Status string `json:"status,omitempty"`
}

// SMIStatuses names the statuses an SMIFault may return. The bridge maps
// each to the library's own enum value.
var SMIStatuses = []string{"", "not_supported", "busy", "timeout", "unknown_error", "no_data", "driver_not_loaded"}

// SMI library names.
const (
	LibraryAmdSmi  = "amdsmi"
	LibraryRocmSmi = "rocmsmi"
)

// GPU is one GPU (one SPX/NPS1 partition: CPX/NPS partitioning is out of
// scope for now).
type GPU struct {
	BDF  string `json:"bdf"` // 0000:05:00.0; the key overrides use
	UUID string `json:"uuid"`
	// Presence is "" for a present GPU, or one of PresenceLost and
	// PresenceFallenOffBus.
	Presence  string        `json:"presence,omitempty"`
	NUMANode  int           `json:"numa_node"`
	Identity  Identity      `json:"identity"`
	KFD       GPUKFD        `json:"kfd"`
	DRM       DRM           `json:"drm"`
	Partition Partition     `json:"partition"`
	ECC       ECC           `json:"ecc"`
	PCIe      PCIe          `json:"pcie"`
	XGMI      XGMI          `json:"xgmi"`
	BadPages  []BadPage     `json:"bad_pages,omitempty"`
	Temps     Temperatures  `json:"temperatures"`
	Power     Power         `json:"power"`
	Metrics   MetricsSubset `json:"metrics"`
}

// GPU presence states. A lost GPU is gone from enumeration and from sysfs; a
// GPU that fell off the bus keeps its PCI entry but every query fails and its
// config space reads all-ones.
const (
	PresenceLost         = "lost"
	PresenceFallenOffBus = "fallen_off_bus"
)

// Identity is static identity: PCI ids, marketing name, firmware.
type Identity struct {
	VendorID          uint32            `json:"vendor_id"`
	DeviceID          uint32            `json:"device_id"`
	SubsystemVendorID uint32            `json:"subsystem_vendor_id"`
	SubsystemID       uint32            `json:"subsystem_id"`
	RevisionID        uint32            `json:"revision_id"`
	PCIClass          uint32            `json:"pci_class"` // 0x120000, processing accelerator
	MarketName        string            `json:"market_name"`
	GFXVersion        string            `json:"gfx_version"` // gfx950
	ASICSerial        string            `json:"asic_serial"`
	VRAMBytes         uint64            `json:"vram_bytes"`
	VBIOS             VBIOS             `json:"vbios"`
	Firmware          map[string]string `json:"firmware,omitempty"` // block -> version
}

// VBIOS identifies the board firmware image.
type VBIOS struct {
	Name       string `json:"name"`
	PartNumber string `json:"part_number"`
	Version    string `json:"version"`
	BuildDate  string `json:"build_date"`
}

// GPUKFD is the GPU's KFD topology node.
type GPUKFD struct {
	GPUID            uint32 `json:"gpu_id"`  // kfd_id in amd-smi list
	NodeID           uint32 `json:"node_id"` // KFD topology node index
	UniqueID         uint64 `json:"unique_id"`
	GFXTargetVersion uint32 `json:"gfx_target_version"` // 90500
	SIMDCount        uint32 `json:"simd_count"`
	ArrayCount       uint32 `json:"array_count"`
	NumXCC           uint32 `json:"num_xcc"`
}

// DRM holds the card/renderD indices. They must not collide with DRM nodes
// that already exist on the host (a cloud VM's virtual display is often
// card0/card1); the node agent remaps a collision by writing new indices
// into the override document, so the library, KFD topology and /dev agree.
type DRM struct {
	Card        int `json:"card"`
	RenderMinor int `json:"render_minor"` // >= 128, renderD<minor>
}

// Partition is the current compute/memory partition mode.
type Partition struct {
	Compute string `json:"compute"` // SPX
	Memory  string `json:"memory"`  // NPS1
}

// ECC holds RAS error counters per block (block names as in ECCBlocks).
type ECC struct {
	Blocks map[string]ECCBlock `json:"blocks"`
}

// ECCBlock is one RAS block. A block absent from the map is not supported.
type ECCBlock struct {
	Disabled      bool   `json:"disabled,omitempty"`
	Correctable   uint64 `json:"correctable"`
	Uncorrectable uint64 `json:"uncorrectable"`
	Deferred      uint64 `json:"deferred"`
	// CorrectablePerHour grows Correctable linearly from RateSince, so a
	// rate-based check sees the counter move between its runs.
	CorrectablePerHour float64 `json:"correctable_per_hour,omitempty"`
	RateSince          string  `json:"rate_since,omitempty"` // RFC 3339
}

// ECCBlocks are the amdsmi_gpu_block_t names, lower-cased.
var ECCBlocks = []string{
	"umc", "sdma", "gfx", "mmhub", "athub", "pcie_bif", "hdp", "xgmi_wafl", "df",
	"smn", "sem", "mp0", "mp1", "fuse", "mca", "vcn", "jpeg", "ih", "mpio",
}

// PCIe is link state and error counters. Speeds are in MT/s (32000 = Gen5).
type PCIe struct {
	Width               int    `json:"width"`
	MaxWidth            int    `json:"max_width"`
	SpeedMTs            int    `json:"speed_mts"`
	MaxSpeedMTs         int    `json:"max_speed_mts"`
	ReplayCount         uint64 `json:"replay_count"`
	ReplayRolloverCount uint64 `json:"replay_rollover_count"`
	NAKSentCount        uint64 `json:"nak_sent_count"`
	NAKReceivedCount    uint64 `json:"nak_received_count"`
	L0ToRecoveryCount   uint64 `json:"l0_to_recovery_count"`
}

// XGMI is the GPU's side of the XGMI fabric. One map feeds both the
// amdsmi_topo_*/is_P2P_accessible answers and the KFD io_links files, so the
// library and sysfs agree by construction.
type XGMI struct {
	HiveID uint64              `json:"hive_id"`
	Links  map[string]XGMILink `json:"links,omitempty"` // keyed by peer BDF
}

// XGMILink is one direct XGMI link to a peer GPU. Validation requires the
// peer to list this GPU too.
type XGMILink struct {
	Down             bool   `json:"down,omitempty"`
	Weight           uint64 `json:"weight"`
	MinBandwidthMBps uint64 `json:"min_bandwidth_mbps"`
	MaxBandwidthMBps uint64 `json:"max_bandwidth_mbps"`
}

// BadPage is one retired-page table entry.
type BadPage struct {
	Address uint64 `json:"address"`
	Size    uint64 `json:"size"`
	Status  string `json:"status"` // one of BadPageStatuses
}

// BadPageStatuses mirrors amdsmi_memory_page_status_t.
var BadPageStatuses = []string{"reserved", "pending", "unreservable"}

// Temperatures maps a sensor to its metrics, both by amdsmi name (see
// TempSensors, TempMetrics). A missing sensor or metric is NOT_SUPPORTED,
// which is how a real part reports e.g. the edge sensor on MI300X.
type Temperatures map[string]map[string]float64

// TempSensors are amdsmi_temperature_type_t names, lower-cased.
var TempSensors = []string{"edge", "hotspot", "vram", "hbm_0", "hbm_1", "hbm_2", "hbm_3", "plx"}

// TempMetrics are amdsmi_temperature_metric_t names, lower-cased.
var TempMetrics = []string{
	"current", "max", "min", "max_hyst", "min_hyst", "critical", "critical_hyst",
	"emergency", "emergency_hyst", "crit_min", "crit_min_hyst", "offset", "lowest",
	"highest", "shutdown",
}

// Power is socket power and the power cap, in watts.
type Power struct {
	SocketW     float64 `json:"socket_w"`
	CapW        float64 `json:"cap_w"`
	DefaultCapW float64 `json:"default_cap_w"`
	MinCapW     float64 `json:"min_cap_w"`
	MaxCapW     float64 `json:"max_cap_w"`
	// PTL is the peak-TOPS limiter (gfx950). Nil means not supported.
	PTL *PTL `json:"ptl,omitempty"`
}

// PTL is the peak-TOPS limiter state.
type PTL struct {
	Enabled bool     `json:"enabled"`
	Formats []string `json:"formats,omitempty"`
}

// MetricsSubset is the part of amdsmi_gpu_metrics_t no other field already
// determines. Temperatures, PCIe counters and XGMI link status in the metrics
// struct are derived from the fields above, never stored twice.
type MetricsSubset struct {
	GFXActivityPct    uint32 `json:"gfx_activity_pct"`
	UMCActivityPct    uint32 `json:"umc_activity_pct"`
	MMActivityPct     uint32 `json:"mm_activity_pct"`
	GFXClockMHz       uint32 `json:"gfx_clock_mhz"`
	MemClockMHz       uint32 `json:"mem_clock_mhz"`
	EnergyAccumulator uint64 `json:"energy_accumulator"`
	XGMILinkWidth     uint32 `json:"xgmi_link_width"`
	XGMILinkSpeedGbps uint32 `json:"xgmi_link_speed_gbps"`
}
