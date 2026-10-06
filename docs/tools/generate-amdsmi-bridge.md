# generate-amdsmi-bridge

Keeps the AMD simulator's replacement libraries ABI-complete. `amd-smi` loads
`libamd_smi.so.26` through `amdsmi_wrapper.py`, which binds every exported
symbol when it is imported, so a library that lacks one export breaks the whole
CLI. `rocm-smi` loads `librocm_smi64.so.1` the same way. The generator emits a
C stub returning `NOT_SUPPORTED` for every export of both libraries.

It reads the ROCm headers and the export lists recorded from the real
libraries, both vendored under `pkg/gpu/mockamdsmi/abi/rocm-<version>/`, and
checks that the two agree before writing anything:

- a function the header declares but the real library does not export fails
  generation;
- an export the header does not declare fails generation, unless it is on the
  generator's known-undeclared list (today only
  `amdsmi_free_name_value_pairs`, which `amdsmi_wrapper.py` binds anyway).

A ROCm bump that adds or removes a symbol therefore surfaces as a generation
error, not as a library that quietly lacks it.

The stubs take no parameters and include no header. Their arguments are never
read, and under the SysV x86-64 calling convention the caller cleans up the
arguments and reads the status from `eax`. A `(void)` definition is
therefore ABI-compatible with every real prototype. Each stub's comment
carries the prototype it stands in for.

## Running it

```bash
make -C pkg/gpu/mockamdsmi generate         # rewrite bridge/{amdsmi,rocmsmi}/stubs_generated.c
make -C pkg/gpu/mockamdsmi check-generated  # fail if the committed stubs are stale
make -C pkg/gpu/mockamdsmi check-abi        # link both stub libraries, diff nm -D against the real exports
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-rocm` | `7.2.1` | ROCm version; reads `<abi>/rocm-<version>/` |
| `-abi` | `pkg/gpu/mockamdsmi/abi` | Directory holding the vendored inputs |
| `-bridge` | `pkg/gpu/mockamdsmi/bridge` | Output root; each library goes to `<bridge>/<lib>/stubs_generated.c` |

## Adding a ROCm version

Copy `amdsmi.h`, `rocm_smi.h` and the two export lists
(`nm -D --defined-only <lib> | awk '$3 ~ /^amdsmi_/ {print $3}' | sort`) from
the new ROCm packages into a sibling `rocm-<version>/` directory, then run
`make -C pkg/gpu/mockamdsmi generate check-abi ROCM=<version>`.
