// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command generate-amdsmi-bridge generates the C export surface of the two
// replacement AMD SMI libraries, libamd_smi.so.26 and librocm_smi64.so.1, from
// the ROCm headers vendored under pkg/gpu/mockamdsmi/abi/rocm-<ver>/.
//
// For each library it parses every prototype with the library's prefix out of
// the header (amdsmi_* from amdsmi.h, rsmi_* from rocm_smi.h), reconciles that
// set with the export list recorded from the real .so, and writes one C file
// in which every export returns NOT_SUPPORTED. Reconciliation is strict: a
// declared function the real library does not export, or an export that is
// neither declared nor on the known-undeclared list, fails generation. A ROCm
// bump that adds or drops a symbol therefore surfaces as an error instead of
// a library that silently lacks it. amdsmi_wrapper.py binds every symbol when
// it is imported, so a single missing export breaks the whole amd-smi CLI.
//
// It emits NOT_SUPPORTED stubs only. The engine-backed implementations of the
// census functions (MEP-1001) will displace their stubs once they exist.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	rocm := flag.String("rocm", "7.2.1", "ROCm version; reads <abi>/rocm-<version>/")
	abi := flag.String("abi", "pkg/gpu/mockamdsmi/abi", "directory holding the vendored rocm-<version>/ inputs")
	bridge := flag.String("bridge", "pkg/gpu/mockamdsmi/bridge", "output root; each library is written to <bridge>/<lib>/stubs_generated.c")
	flag.Parse()

	for _, lib := range Libraries {
		if err := generate(filepath.Join(*abi, "rocm-"+*rocm), *bridge, *rocm, lib); err != nil {
			fmt.Fprintf(os.Stderr, "generate-amdsmi-bridge: %s: %v\n", lib.Soname, err)
			os.Exit(1)
		}
	}
}

func generate(in, bridge, rocm string, lib Library) error {
	header, err := os.ReadFile(filepath.Join(in, lib.Header))
	if err != nil {
		return fmt.Errorf("reading header: %w", err)
	}
	exports, err := os.ReadFile(filepath.Join(in, lib.Soname+".exports"))
	if err != nil {
		return fmt.Errorf("reading export list: %w", err)
	}
	syms, err := Reconcile(lib, ParsePrototypes(string(header), lib.Prefix), ParseExports(string(exports)))
	if err != nil {
		return err
	}
	dir := filepath.Join(bridge, lib.Dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, "stubs_generated.c")
	if err := os.WriteFile(path, []byte(RenderC(lib, rocm, syms)), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "%s: %d exports (%d declared in %s) -> %s\n",
		lib.Soname, len(syms), countDeclared(syms), lib.Header, path)
	return nil
}
