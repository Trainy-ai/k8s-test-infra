// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Library describes one replacement shared library.
type Library struct {
	Soname string // e.g. libamd_smi.so.26; also names the .exports file
	Dir    string // output directory under -bridge
	Header string // path under third_party/rocm-<ver>/
	Prefix string // exported symbol prefix
	// NotSupported is the library's NOT_SUPPORTED status value. Both enums
	// put it at 2 (AMDSMI_STATUS_NOT_SUPPORTED = 2; RSMI_STATUS_NOT_SUPPORTED
	// is the third member after SUCCESS = 0 and INVALID_ARGS).
	NotSupported int
	// Undeclared lists symbols the real library exports without declaring
	// them in its public header. Anything else undeclared is an error.
	Undeclared map[string]string
}

// Libraries is the set the generator emits, in output order.
var Libraries = []Library{
	{
		Soname: "libamd_smi.so.26", Dir: "amdsmi", Header: "include/amd_smi/amdsmi.h",
		Prefix: "amdsmi_", NotSupported: 2,
		Undeclared: map[string]string{
			// impl/amd_smi_utils.h; amdsmi_wrapper.py binds it (restype None).
			"amdsmi_free_name_value_pairs": "void amdsmi_free_name_value_pairs(void *p)",
		},
	},
	{
		Soname: "librocm_smi64.so.1", Dir: "rocmsmi", Header: "include/rocm_smi/rocm_smi.h",
		Prefix: "rsmi_", NotSupported: 2,
	},
}

// Symbol is one export with the prototype that declares it.
type Symbol struct {
	Name      string
	Prototype string // whitespace-normalised C prototype, no trailing ';'
	Declared  bool   // false for Library.Undeclared entries
}

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`//[^\n]*`)
	preprocessor = regexp.MustCompile(`(?m)^[ \t]*#[^\n]*`)
	spaces       = regexp.MustCompile(`\s+`)
)

// ParsePrototypes returns every function declared with prefix in a C header,
// keyed by name. Preprocessor lines are dropped rather than evaluated, so both
// arms of every #ifdef count; that is deliberate, because the real library is
// built with ENABLE_ESMI_LIB and exports the CPU (ESMI) half too.
func ParsePrototypes(header, prefix string) map[string]string {
	text := blockComment.ReplaceAllString(header, " ")
	text = lineComment.ReplaceAllString(text, "")
	text = preprocessor.ReplaceAllString(text, "")
	decl := regexp.MustCompile(`(?s)^\s*([A-Za-z_][\w\s*]*?)\b(` + regexp.QuoteMeta(prefix) + `\w+)\s*\((.*)\)\s*$`)
	out := map[string]string{}
	for _, chunk := range regexp.MustCompile(`[;{}]`).Split(text, -1) {
		m := decl.FindStringSubmatch(chunk)
		if m == nil || strings.Contains(m[1], "typedef") {
			continue
		}
		out[m[2]] = strings.TrimSpace(spaces.ReplaceAllString(m[0], " "))
	}
	return out
}

// ParseExports parses a one-symbol-per-line export list.
func ParseExports(list string) []string {
	var out []string
	for _, line := range strings.Split(list, "\n") {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, s)
		}
	}
	return out
}

// Reconcile checks that the declared and exported sets agree, and returns the
// export list (sorted) with a prototype for each symbol.
func Reconcile(lib Library, declared map[string]string, exports []string) ([]Symbol, error) {
	var problems []string
	exported := map[string]bool{}
	var syms []Symbol
	for _, name := range exports {
		if !strings.HasPrefix(name, lib.Prefix) {
			problems = append(problems, fmt.Sprintf("export %s lacks prefix %s", name, lib.Prefix))
			continue
		}
		exported[name] = true
		switch {
		case declared[name] != "":
			syms = append(syms, Symbol{Name: name, Prototype: declared[name], Declared: true})
		case lib.Undeclared[name] != "":
			syms = append(syms, Symbol{Name: name, Prototype: lib.Undeclared[name]})
		default:
			problems = append(problems, fmt.Sprintf("%s is exported but not declared in %s and not in the known-undeclared list", name, lib.Header))
		}
	}
	for name := range declared {
		if !exported[name] {
			problems = append(problems, fmt.Sprintf("%s is declared in %s but the real library does not export it", name, lib.Header))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("header and export list disagree:\n  %s", strings.Join(problems, "\n  "))
	}
	sort.Slice(syms, func(i, j int) bool { return syms[i].Name < syms[j].Name })
	return syms, nil
}

// RenderC renders the NOT_SUPPORTED stub translation unit.
//
// Stubs take no parameters on purpose. Their arguments are never read, the
// SysV x86-64 calling convention leaves argument cleanup to the caller, and
// the status comes back in eax, so a (void) definition is ABI-compatible with
// every real prototype. Not including the vendored header also keeps libdrm
// (which rocm_smi.h pulls in through kfd_ioctl.h) out of the build.
func RenderC(lib Library, rocm string, syms []Symbol) string {
	var b strings.Builder
	b.WriteString("// Copyright 2026 Trainy, Inc.\n// SPDX-License-Identifier: Apache-2.0\n\n")
	fmt.Fprintf(&b, "// Code generated by cmd/generate-amdsmi-bridge from abi/rocm-%s; DO NOT EDIT.\n", rocm)
	fmt.Fprintf(&b, "//\n// %s: %d exports, each returning NOT_SUPPORTED (%d).\n", lib.Soname, len(syms), lib.NotSupported)
	b.WriteString("// The prototype each stub stands in for is in the comment above it.\n\n")
	for _, s := range syms {
		fmt.Fprintf(&b, "/* %s */\nint %s(void) { return %d; }\n", s.Prototype, s.Name, lib.NotSupported)
	}
	return b.String()
}

func countDeclared(syms []Symbol) int {
	n := 0
	for _, s := range syms {
		if s.Declared {
			n++
		}
	}
	return n
}
