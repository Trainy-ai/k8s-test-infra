# mockamdsmi: AMD GPU simulator

Simulates AMD Instinct GPUs for the real `amd-smi`, `rocm-smi` and
`rocm/k8s-device-plugin`, the way `mocknvml` does for NVIDIA. The design,
the Wave 0 evidence it rests on and the execution plan are in
[MEP-1001](../../../enhancements/meps/1001-amd-gpu-simulator/README.md).

| Package | What it owns |
|---|---|
| [`profile`](profile/) | Node profile schema, loading, validation |
| [`overrides`](overrides/) | Runtime fault-state document and its change detection |
| [`engine`](engine/) | The `Query` interface both libraries and the sysfs renderer read; the call census |
| [`faults`](faults/) | The fault catalog, compiled to override patches and one-shot actions |
| [`sysfs`](sysfs/) | Paths the consumers read, overlay mount points, the in-place write rule |
| [`configs`](configs/) | Example profiles (synthetic until replaced by hardware captures) |
| [`abi`](abi/) | Vendored ROCm headers and the real libraries' export lists |
| [`bridge`](bridge/) | Generated `NOT_SUPPORTED` stubs for both libraries ([generator](../../../docs/tools/generate-amdsmi-bridge.md)) |

Profiles come from real nodes through [`tools/amd-capture`](../../../tools/amd-capture/README.md).

```bash
make -C pkg/gpu/mockamdsmi test   # go vet, gofmt, go test (AMD paths only)
make -C pkg/gpu/mockamdsmi lint   # golangci-lint with the repository config
make -C pkg/gpu/mockamdsmi check-generated check-abi
```
