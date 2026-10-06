# amd-capture

`capture.sh` records one real AMD GPU node, read-only. The output is the
source for the [AMD simulator's](../../enhancements/meps/1001-amd-gpu-simulator/README.md)
MI355X/MI300X profiles (`pkg/gpu/mockamdsmi/configs/`) and for the golden
`amd-smi`/`rocm-smi` outputs that the contract tests compare against.

**A human runs this on real hardware.** Nothing in CI runs it, and it has not
yet been run against a real node or cluster.

## What it does (and doesn't)

| Captures | How |
|---|---|
| Every `amd-smi`/`rocm-smi` invocation that konduktor's `docker/trainy-npd/checks/amd_*.py` makes (`cmd/check-NN.*`), plus `hipconfig --version` | Runs each one under `timeout`, saving stdout, stderr, the rc and the wall time in ms |
| Fuller dumps for profiles: `static`, `metric`, `firmware`, `topology`, `xgmi`, `partition`, `list -e`, `rocm-smi --showallinfo` (`cmd/profile-NN.*`) | Same |
| sysfs: `/sys/module/amdgpu`, `/sys/class/kfd`, `/sys/devices/platform/amdgpu_xcp_*`, every AMD PCI function in full (`ras/`, `hwmon/`, `drm/`, `fw_version/`, `xgmi_hive_info/`, `power/`), and the link width/speed of every upstream PCIe bridge | `cat`, with each read under a 3s timeout. Symlinks are recorded in `meta/symlinks.txt` and never followed. |
| `vendor`/`device`/`class` for **every** PCI function (the konduktor classifier reads these) | Same |
| `/dev/kfd` and `/dev/dri/*` type, major:minor, mode and owner | `stat` only. **The nodes are never opened.** |
| The `hypervisor` CPU flag | It changes which `amd-smi` subcommands exist (see below) |
| Versions: kernel, amdgpu module, ROCm `.info/version`, packages, and the sha256 of every `libamd_smi.so*`/`librocm_smi64.so*`/`amdsmi_wrapper.py` | |
| `amdgpu`/KFD/DRM/RAS/XGMI lines from the kernel log | `dmesg -T` (falls back to `journalctl -k`). Needs root. |

It never writes to sysfs and never runs `amd-smi set`, `reset`, `ras --cper`,
`process` or `event`. It also skips files whose read has side effects or is not
plain data: `resource*`, `rom`, `reset`, `remove`, `rescan`, `ras_ctrl`,
`pp_od_clk_voltage`, and so on. It writes only to `OUTDIR`.

Expect a few minutes on an 8-GPU node, mostly the SMI calls, and one tarball
per node of a few MB. These are estimates: it has only run against the Wave 0
stub libraries and a staged sysfs, where it took about 4 seconds.

## Running it

On the node itself (preferred; this is how `amd-smi` sees the hardware):

```bash
sudo ./capture.sh /tmp            # prints /tmp/amd-capture-<host>-<utc>.tar.gz
```

From a cluster, use the node-problem-detector pod that already runs
privileged with the host mounted at `/host`:

```bash
kubectl -n <npd-ns> cp capture.sh <npd-pod>:/host/tmp/capture.sh
kubectl -n <npd-ns> exec <npd-pod> -- chroot /host bash /tmp/capture.sh /tmp
kubectl -n <npd-ns> cp <npd-pod>:/host/tmp/amd-capture-<...>.tar.gz .
```

Environment knobs: `AMD_SMI=/path/amd-smi`, `ROCM_SMI=/path/rocm-smi`,
`CMD_TIMEOUT` (default 90s per command) and `READ_TIMEOUT` (default 3s per
sysfs file).

## Which nodes

- **MI355X** (AMP standard) and **MI300X**, plus **MI325X** if we have one.
  Run it on at least one healthy node per SKU and ROCm version.
- **A node with known faults**, if one exists: retired or pending bad pages,
  non-zero ECC counts, a degraded PCIe link, or an XGMI error. These become the
  fault goldens.
- Note which ROCm/amd-smi version each node runs. The version matrix floor
  comes from this.

## Privacy

The capture contains hardware serials and UUIDs (`asic_serial`, `unique_id`,
board serials in `static`), hostnames, and kernel log lines. Treat it as
internal. Serials are normalised when a profile is generated from it, before
anything is committed.

## Why `hypervisor` matters

`amd-smi` reads `/proc/cpuinfo`. When the `hypervisor` flag is set and no PCI
function has a known passthrough device id, it treats the host as a VM. In that
case the parser does not register `bad-pages` at all, and `static --limit` is
rejected as an invalid parameter. The Wave 0 census reproduced this on an Azure VM.
The capture records the flag so the simulator can reproduce bare-metal
behaviour, and so a VM-hosted capture can be spotted.
