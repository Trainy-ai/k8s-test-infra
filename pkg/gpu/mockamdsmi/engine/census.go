// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package engine

// The Wave 0 call census (MEP-1001): every library function the real
// amd-smi 26.2.2 and rocm-smi (rocm-smi-lib 7.8.0) CLIs call for the
// invocations konduktor's AMD health checks make, and the Query method that
// answers it. These, and only these, get engine-backed implementations; every
// other export stays a generated NOT_SUPPORTED stub.
//
// "-" marks plumbing that needs no engine state beyond enumeration.

// CensusAmdSmi maps the 25 libamd_smi functions to their Query source.
var CensusAmdSmi = map[string]string{
	// plumbing, called on every invocation
	"amdsmi_init":                  "-",
	"amdsmi_shut_down":             "-",
	"amdsmi_get_socket_handles":    "GPUs",
	"amdsmi_get_processor_handles": "GPUs",
	"amdsmi_get_gpu_device_bdf":    "GPUs",
	"amdsmi_get_gpu_device_uuid":   "GPU.UUID",
	"amdsmi_get_gpu_kfd_info":      "GPU.KFD",
	// version
	"amdsmi_get_lib_version":     "Node.Software.AmdSmiLibVersion",
	"amdsmi_get_gpu_driver_info": "Node.Driver.Version", // == /sys/module/amdgpu/version
	// metric --ecc, monitor -e
	"amdsmi_get_gpu_total_ecc_count": "ECCTotal",
	"amdsmi_get_gpu_ecc_count":       "ECC",
	"amdsmi_get_gpu_metrics_info":    "GPU (Temps, PCIe, XGMI, Power, Metrics)",
	// metric --pcie, monitor -e
	"amdsmi_get_pcie_info":              "GPU.PCIe",
	"amdsmi_get_gpu_pci_throughput":     "GPU.PCIe",
	"amdsmi_get_gpu_pci_replay_counter": "GPU.PCIe.ReplayCount",
	// topology -a -t
	"amdsmi_is_P2P_accessible":                       "Link.Accessible",
	"amdsmi_topo_get_link_type":                      "Link.Type",
	"amdsmi_topo_get_link_weight":                    "Link.Weight",
	"amdsmi_topo_get_p2p_status":                     "Link",
	"amdsmi_get_minmax_bandwidth_between_processors": "Link.MinBWMBps/MaxBWMBps",
	// bad-pages
	"amdsmi_get_gpu_bad_page_info": "GPU.BadPages",
	// metric --temperature, static --limit
	"amdsmi_get_temp_metric":         "Temperature",
	"amdsmi_get_supported_power_cap": "GPU.Power",
	"amdsmi_get_gpu_ptl_formats":     "GPU.Power.PTL",
	"amdsmi_get_gpu_ptl_state":       "GPU.Power.PTL",
}

// RecommendedAmdSmi are not in the census but serve neighbouring
// subcommands (list -e, static --asic) cheaply.
var RecommendedAmdSmi = map[string]string{
	"amdsmi_get_gpu_enumeration_info":      "GPU.DRM, GPU.KFD",
	"amdsmi_get_gpu_asic_info":             "GPU.Identity",
	"amdsmi_get_processor_handle_from_bdf": "GPUs",
	"amdsmi_get_socket_info":               "GPUs",
}

// TolerantAmdSmi are called but the CLI handles NOT_SUPPORTED cleanly, so
// they stay stubs.
var TolerantAmdSmi = []string{
	"amdsmi_get_node_handle",
	"amdsmi_get_cpu_handles",
	"amdsmi_get_gpu_virtualization_mode", // VM mode only; the simulator presents bare metal
}

// CensusRocmSmi maps the 13 librocm_smi64 functions to their Query source.
// rocm-smi addresses GPUs by index: index i is GPUs()[i].
var CensusRocmSmi = map[string]string{
	"rsmi_init":                "-",
	"rsmi_shut_down":           "-",
	"rsmi_status_string":       "-",
	"rsmi_num_monitor_devices": "GPUs",
	"rsmi_dev_id_get":          "GPU.Identity.DeviceID",
	// Must return 0x1002, or every rocm-smi command prints "No AMD GPUs
	// specified" and still exits 0.
	"rsmi_dev_vendor_id_get":   "GPU.Identity.VendorID",
	"rsmi_dev_unique_id_get":   "GPU.KFD.UniqueID",
	"rsmi_version_get":         "Node.Software.RocmSmiLibVersion",
	"rsmi_dev_ecc_status_get":  "ECC",
	"rsmi_dev_ecc_count_get":   "ECC",
	"rsmi_is_P2P_accessible":   "Link.Accessible",
	"rsmi_topo_get_link_type":  "Link.Type",
	"rsmi_dev_temp_metric_get": "Temperature",
}
