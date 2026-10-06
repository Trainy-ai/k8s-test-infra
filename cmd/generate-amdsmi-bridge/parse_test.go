// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleHeader = `
typedef enum { AMDSMI_STATUS_SUCCESS = 0, AMDSMI_STATUS_NOT_SUPPORTED = 2 } amdsmi_status_t;
typedef amdsmi_status_t (*amdsmi_cb_t)(int);  /* not a declaration */
// amdsmi_commented_out(int x);
amdsmi_status_t amdsmi_init(uint64_t init_flags);
amdsmi_status_t
amdsmi_get_gpu_device_bdf(amdsmi_processor_handle processor_handle,
                          amdsmi_bdf_t *bdf);
#ifdef ENABLE_ESMI_LIB
amdsmi_status_t amdsmi_get_cpu_handles(uint32_t *count, amdsmi_processor_handle *handles);
#endif
amdsmi_status_t amdsmi_is_P2P_accessible(amdsmi_processor_handle a, amdsmi_processor_handle b, bool *accessible);
`

func TestParsePrototypes(t *testing.T) {
	t.Parallel()

	got := ParsePrototypes(sampleHeader, "amdsmi_")
	require.Equal(t, map[string]string{
		"amdsmi_init": "amdsmi_status_t amdsmi_init(uint64_t init_flags)",
		"amdsmi_get_gpu_device_bdf": "amdsmi_status_t amdsmi_get_gpu_device_bdf(" +
			"amdsmi_processor_handle processor_handle, amdsmi_bdf_t *bdf)",
		// Both arms of an #ifdef count: the real library is built with ESMI.
		"amdsmi_get_cpu_handles": "amdsmi_status_t amdsmi_get_cpu_handles(uint32_t *count, amdsmi_processor_handle *handles)",
		// Mixed case survives (an [a-z]-only scan once missed it).
		"amdsmi_is_P2P_accessible": "amdsmi_status_t amdsmi_is_P2P_accessible(" +
			"amdsmi_processor_handle a, amdsmi_processor_handle b, bool *accessible)",
	}, got)
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	lib := Library{
		Soname: "libx.so", Header: "x.h", Prefix: "amdsmi_",
		Undeclared: map[string]string{"amdsmi_hidden": "void amdsmi_hidden(void *p)"},
	}
	declared := map[string]string{"amdsmi_b": "int amdsmi_b(void)", "amdsmi_a": "int amdsmi_a(int)"}

	syms, err := Reconcile(lib, declared, []string{"amdsmi_b", "amdsmi_hidden", "amdsmi_a"})
	require.NoError(t, err)
	require.Equal(t, []Symbol{
		{Name: "amdsmi_a", Prototype: "int amdsmi_a(int)", Declared: true},
		{Name: "amdsmi_b", Prototype: "int amdsmi_b(void)", Declared: true},
		{Name: "amdsmi_hidden", Prototype: "void amdsmi_hidden(void *p)"},
	}, syms)

	_, err = Reconcile(lib, declared, []string{"amdsmi_a"})
	require.ErrorContains(t, err, "amdsmi_b is declared in x.h but the real library does not export it")

	_, err = Reconcile(lib, declared, []string{"amdsmi_a", "amdsmi_b", "amdsmi_new"})
	require.ErrorContains(t, err, "amdsmi_new is exported but not declared")
}

// TestVendoredABI pins the census of the vendored ROCm 7.2.1 inputs: every
// export of the real libraries is accounted for, and the counts match the
// Wave 0 spike (192 amdsmi, 116 rsmi).
func TestVendoredABI(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("..", "..", "pkg", "gpu", "mockamdsmi", "abi", "rocm-7.2.1")
	want := map[string][2]int{"libamd_smi.so.26": {192, 191}, "librocm_smi64.so.1": {116, 116}}
	for _, lib := range Libraries {
		header, err := os.ReadFile(filepath.Join(dir, lib.Header))
		require.NoError(t, err)
		exports, err := os.ReadFile(filepath.Join(dir, lib.Soname+".exports"))
		require.NoError(t, err)

		syms, err := Reconcile(lib, ParsePrototypes(string(header), lib.Prefix), ParseExports(string(exports)))
		require.NoError(t, err, lib.Soname)
		require.Len(t, syms, want[lib.Soname][0], lib.Soname)
		require.Equal(t, want[lib.Soname][1], countDeclared(syms), lib.Soname)
	}
}

func TestRenderCIsDeterministic(t *testing.T) {
	t.Parallel()

	lib := Libraries[1]
	syms := []Symbol{{Name: "rsmi_init", Prototype: "rsmi_status_t rsmi_init(uint64_t init_flags)", Declared: true}}
	out := RenderC(lib, "7.2.1", syms)
	require.Contains(t, out, "/* rsmi_status_t rsmi_init(uint64_t init_flags) */\nint rsmi_init(void) { return 2; }\n")
	require.Equal(t, out, RenderC(lib, "7.2.1", syms))
}
