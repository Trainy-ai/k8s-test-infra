# Vendored ROCm 7.2.1 ABI inputs

Unmodified copies from the pinned ROCm 7.2.1 noble debs.
`cmd/generate-amdsmi-bridge` reads them; nothing here is compiled. The
`*.license` sidecars carry the REUSE annotation, because the repository-wide
`REUSE.toml` globs would otherwise label these AMD files Apache-2.0.

| File | Source | sha256 |
|---|---|---|
| `include/amd_smi/amdsmi.h` | `amd-smi-lib_26.2.2.70201-81~24.04_amd64.deb` | `e9502a3ff54c13c5…` |
| `include/rocm_smi/rocm_smi.h` | `rocm-smi-lib_7.8.0.70201-81~24.04_amd64.deb` | `49dfd19959d11892…` |
| `libamd_smi.so.26.exports` | `nm -D --defined-only libamd_smi.so.26.2.2`, `amdsmi_*` only (192) | lib `86a9ebd926386986…` |
| `librocm_smi64.so.1.exports` | `nm -D --defined-only librocm_smi64.so.1.0.70201`, `rsmi_*` only (116) | lib `cca245677e869de8…` |

The debs are at
`https://repo.radeon.com/rocm/apt/7.2.1/pool/main/{a/amd-smi-lib,r/rocm-smi-lib}/`
(deb sha256 `56c41997…` and `9aaef95b…`).

The `.exports` lists are the ABI contract. `amdsmi_wrapper.py` binds every
symbol at import time, so a replacement library must export all of them, not
only the ones it implements. The headers declare 191 of the 192 amdsmi
exports. The one exception, `amdsmi_free_name_value_pairs`, is internal
(`impl/amd_smi_utils.h`), but the wrapper still binds it.

To add a ROCm version, add a sibling `rocm-<ver>/` directory with the same
files, then run `make -C pkg/gpu/mockamdsmi generate ROCM=<ver>`.

Licenses: MIT, see `LICENSE.amd-smi-lib.txt` and `LICENSE.rocm-smi-lib.md`.
