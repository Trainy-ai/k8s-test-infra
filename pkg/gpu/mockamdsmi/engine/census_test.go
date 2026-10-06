// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package engine_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/engine"
)

// TestCensusIsExported checks every census function against the export
// lists recorded from the real ROCm 7.2.1 libraries: a census entry the
// library does not export is a typo or a ROCm drift, never a real call.
func TestCensusIsExported(t *testing.T) {
	t.Parallel()

	read := func(name string) map[string]bool {
		data, err := os.ReadFile("../abi/rocm-7.2.1/" + name)
		require.NoError(t, err)
		out := map[string]bool{}
		for _, s := range strings.Fields(string(data)) {
			out[s] = true
		}
		return out
	}
	amd, rsmi := read("libamd_smi.so.26.exports"), read("librocm_smi64.so.1.exports")

	for fn := range engine.CensusAmdSmi {
		require.True(t, amd[fn], fn)
	}
	for fn := range engine.RecommendedAmdSmi {
		require.True(t, amd[fn], fn)
	}
	for _, fn := range engine.TolerantAmdSmi {
		require.True(t, amd[fn], fn)
	}
	for fn := range engine.CensusRocmSmi {
		require.True(t, rsmi[fn], fn)
	}
}
