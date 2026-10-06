#!/usr/bin/env bash
# Copyright 2026 Trainy, Inc.
# SPDX-License-Identifier: Apache-2.0
#
# amd-capture: read-only snapshot of one AMD GPU node, for building the AMD
# simulator's profiles and golden amd-smi/rocm-smi outputs (MEP-1001). See
# README.md.
#
#   sudo ./capture.sh [OUTDIR]        -> OUTDIR/amd-capture-<host>-<utc>.tar.gz
#
# Read-only by construction: it only reads sysfs/procfs, stats /dev nodes (never
# opens them), and runs amd-smi/rocm-smi query subcommands. Every read and
# command is under a timeout, because sysfs reads and SMI calls can hang on a
# wedged GPU. It writes nothing outside OUTDIR.
set -u
umask 022
OUTROOT=${1:-$PWD}
CMD_TIMEOUT=${CMD_TIMEOUT:-90}
READ_TIMEOUT=${READ_TIMEOUT:-3}
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
HOST=$(hostname -s 2>/dev/null || echo unknown)
NAME=amd-capture-$HOST-$STAMP
OUT=$OUTROOT/$NAME
mkdir -p "$OUT"/{cmd,sys,meta} || exit 1
log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$OUT/meta/capture.log" >&2; }

AMD_SMI=${AMD_SMI:-$(command -v amd-smi || ls /opt/rocm/bin/amd-smi 2>/dev/null)}
ROCM_SMI=${ROCM_SMI:-$(command -v rocm-smi || ls /opt/rocm/bin/rocm-smi 2>/dev/null)}

# --- 1. commands -------------------------------------------------------------
# The first block is exactly what konduktor's docker/trainy-npd/checks/amd_*.py
# run; the second is extra profile material (full static/metric/topology dumps).
CHECK_CMDS=(
  "amd-smi list --json"
  "amd-smi version"
  "amd-smi metric --ecc --json"
  "amd-smi metric --pcie --json"
  "amd-smi monitor -e"
  "amd-smi topology -a -t --json"
  "amd-smi bad-pages"
  "amd-smi bad-pages --json"
  "amd-smi metric --temperature --json"
  "amd-smi static --limit --json"
  "rocm-smi --showrasinfo all"
  "rocm-smi --showtopoaccess --json"
  "rocm-smi --showtopotype --json"
  "rocm-smi --showuniqueid --json"
  "rocm-smi --version"
  "rocm-smi --showtemp --json"
  "hipconfig --version"
)
PROFILE_CMDS=(
  "amd-smi version --json"
  "amd-smi list -e --json"
  "amd-smi static --json"
  "amd-smi metric --json"
  "amd-smi metric --ecc-blocks --json"
  "amd-smi firmware --json"
  "amd-smi topology --json"
  "amd-smi xgmi --json"
  "amd-smi partition --json"
  "amd-smi monitor -e --json"
  "amd-smi metric --ecc"
  "amd-smi metric --pcie"
  "amd-smi metric --temperature"
  "amd-smi static --limit"
  "amd-smi list"
  "amd-smi topology -a -t"
  "rocm-smi --showtopo --json"
  "rocm-smi --showallinfo --json"
)
run() {  # run <index> <command string>
  local idx=$1 cmd=$2 bin rest
  bin=${cmd%% *}; rest=${cmd#"$bin"}
  case $bin in amd-smi) bin=$AMD_SMI ;; rocm-smi) bin=$ROCM_SMI ;; esac
  local base="$OUT/cmd/$idx"
  printf '%s\n' "$cmd" > "$base.cmd"
  if [ -z "$bin" ] || ! command -v "$bin" >/dev/null 2>&1; then
    echo "not-installed" > "$base.rc"; log "skip (not installed): $cmd"; return
  fi
  local t0 t1
  t0=$(date +%s%N)
  # shellcheck disable=SC2086  # word splitting of the argument string is intended
  timeout -k 5 "$CMD_TIMEOUT" "$bin" $rest > "$base.out" 2> "$base.err" < /dev/null
  echo $? > "$base.rc"
  t1=$(date +%s%N)
  echo $(( (t1 - t0) / 1000000 )) > "$base.ms"
  log "rc=$(cat "$base.rc") $(cat "$base.ms")ms: $cmd"
}
i=0
for c in "${CHECK_CMDS[@]}"; do i=$((i+1)); run "$(printf 'check-%02d' $i)" "$c"; done
i=0
for c in "${PROFILE_CMDS[@]}"; do i=$((i+1)); run "$(printf 'profile-%02d' $i)" "$c"; done

# --- 2. sysfs ------------------------------------------------------------------
# Copies file *contents* (each read under a timeout) into sys/, mirroring the
# path. Symlinks are recorded as symlinks (meta/symlinks.txt), never followed,
# so sysfs loops cannot run away. Write-only / mmap-only / side-effect files
# are skipped by name.
SKIP_RE='/(resource[0-9]*(_wc)?|rom|remove|rescan|reset|reset_method|new_id|remove_id|bind|unbind|uevent_trigger|driver_override|gpu_reset|amdgpu_(reset|test).*|pp_od_clk_voltage|pp_force_state|ras_ctrl)$'
copy_file() {
  local f=$1 dst="$OUT/sys${1#/sys}"
  [[ $f =~ $SKIP_RE ]] && return
  [ -r "$f" ] || return
  mkdir -p "$(dirname "$dst")"
  timeout -k 1 "$READ_TIMEOUT" cat "$f" > "$dst" 2>/dev/null || {
    echo "$? $f" >> "$OUT/meta/unreadable.txt"; }
}
copy_tree() {  # copy_tree <dir> [maxdepth]
  local d=$1 depth=${2:-12}
  [ -e "$d" ] || { echo "absent $d" >> "$OUT/meta/absent.txt"; return; }
  find "$d" -maxdepth "$depth" -type l -printf '%p -> %l\n' >> "$OUT/meta/symlinks.txt" 2>/dev/null
  find "$d" -maxdepth "$depth" -type f 2>/dev/null | while IFS= read -r f; do copy_file "$f"; done
}
log "sysfs: module, kfd topology, platform xcp"
copy_tree /sys/module/amdgpu 3
copy_tree /sys/class/kfd
find /sys/devices/platform -maxdepth 1 -name 'amdgpu_xcp_*' 2>/dev/null | while read -r d; do copy_tree "$d" 3; done

log "sysfs: every PCI function (vendor/device/class only), AMD functions in full"
for dev in /sys/bus/pci/devices/*; do
  bdf=$(basename "$dev")
  printf '%s -> %s\n' "$dev" "$(readlink "$dev")" >> "$OUT/meta/symlinks.txt"
  for f in vendor device class subsystem_vendor subsystem_device revision; do
    [ -e "$dev/$f" ] && copy_file "/sys/bus/pci/devices/$bdf/$f"
  done
  [ "$(cat "$dev/vendor" 2>/dev/null)" = 0x1002 ] || continue
  real=$(readlink -f "$dev")
  echo "$bdf $real" >> "$OUT/meta/amd-pci-functions.txt"
  # the function itself: top-level files plus the subdirs the checks/sim use
  find "$dev/" -maxdepth 1 -type f | while IFS= read -r f; do copy_file "$f"; done
  for sub in ras power hwmon drm fw_version xgmi_hive_info link; do
    [ -d "$dev/$sub" ] && copy_tree "$dev/$sub" 4
  done
  # upstream PCIe chain: link width/speed at each bridge (degraded links show here)
  p=$(dirname "$real")
  while [[ $p == /sys/devices/pci* ]]; do
    for f in current_link_width current_link_speed max_link_width max_link_speed vendor device class; do
      [ -r "$p/$f" ] && { mkdir -p "$OUT/sys/upstream$p"; timeout "$READ_TIMEOUT" cat "$p/$f" > "$OUT/sys/upstream$p/$f" 2>/dev/null; }
    done
    p=$(dirname "$p")
  done
done
log "sysfs: drm class"
for d in /sys/class/drm/*; do printf '%s -> %s\n' "$d" "$(readlink "$d")" >> "$OUT/meta/symlinks.txt"; done

# --- 3. /dev, /proc, versions, kernel log --------------------------------------
log "dev nodes (stat only, never opened), cpu flags, versions"
ls -ln /dev/kfd /dev/dri > "$OUT/meta/dev.txt" 2>&1
stat -c '%n %F %t:%T %a %U:%G' /dev/kfd /dev/dri/* >> "$OUT/meta/dev.txt" 2>&1
grep -m1 '^flags' /proc/cpuinfo > "$OUT/meta/cpuinfo-flags.txt" 2>/dev/null
grep -m1 '^model name' /proc/cpuinfo >> "$OUT/meta/cpuinfo-flags.txt" 2>/dev/null
grep -c hypervisor /proc/cpuinfo > "$OUT/meta/hypervisor-flag-count.txt" 2>/dev/null
{
  echo "uname: $(uname -a)"
  echo "os-release:"; cat /etc/os-release 2>/dev/null
  echo "amdgpu module version: $(cat /sys/module/amdgpu/version 2>/dev/null)"
  echo "amdgpu initstate: $(cat /sys/module/amdgpu/initstate 2>/dev/null)"
  echo "rocm .info/version: $(cat /opt/rocm/.info/version 2>/dev/null)"
  echo "amd-smi path: $AMD_SMI -> $(readlink -f "$AMD_SMI" 2>/dev/null)"
  echo "rocm-smi path: $ROCM_SMI -> $(readlink -f "$ROCM_SMI" 2>/dev/null)"
  echo "python3: $(python3 --version 2>&1)"
} > "$OUT/meta/versions.txt"
# which libamd_smi.so the CLI would load, and fingerprints of every copy (ABI drift)
find /opt /usr /lib /lib64 -xdev \( -name 'libamd_smi.so*' -o -name 'librocm_smi64.so*' -o -name 'librocm-core.so*' -o -name amdsmi_wrapper.py \) \
  2>/dev/null | while read -r f; do
  printf '%s %s\n' "$(sha256sum "$f" 2>/dev/null | cut -c1-16)" "$(ls -l "$f")"
done > "$OUT/meta/smi-libs.txt"
(dpkg -l 2>/dev/null | grep -Ei 'amd|rocm|hip' || rpm -qa 2>/dev/null | grep -Ei 'amd|rocm|hip') > "$OUT/meta/packages.txt"
lspci -nn -D -d 1002: > "$OUT/meta/lspci.txt" 2>&1
lspci -vv -D -d 1002: > "$OUT/meta/lspci-vv.txt" 2>&1
log "kernel log (amdgpu/kfd/drm/RAS/XGMI lines only)"
KRE='amdgpu|amdkfd|kfd|\[drm\]|drm:|xgmi|ras|mce|ecc|page retirement|bad page|psp|smu|ring .* timeout|gpu reset'
(timeout 20 dmesg -T 2>/dev/null || timeout 20 journalctl -k --no-pager -o short-iso 2>/dev/null) \
  | grep -Ei "$KRE" > "$OUT/meta/kernel-amdgpu.log"
[ -s "$OUT/meta/kernel-amdgpu.log" ] || echo "dmesg unavailable (need root / kernel.dmesg_restrict=0)" >> "$OUT/meta/capture.log"

# --- 4. package ------------------------------------------------------------------
cp "$0" "$OUT/meta/capture.sh" 2>/dev/null
log "packing"
tar -C "$OUTROOT" -czf "$OUT.tar.gz" "$NAME" && rm -rf "$OUT"
echo "$OUT.tar.gz"
