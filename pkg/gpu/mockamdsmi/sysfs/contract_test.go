// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package sysfs_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/k8s-test-infra/pkg/gpu/mockamdsmi/sysfs"
)

func TestPathsLieUnderOverlays(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, p := range sysfs.Paths {
		require.False(t, seen[p.Pattern], "duplicate %s", p.Pattern)
		seen[p.Pattern] = true
		require.NotEmpty(t, p.Source, p.Pattern)
		require.NotEmpty(t, p.Readers, p.Pattern)
		covered := false
		for _, m := range sysfs.OverlayMounts {
			covered = covered || strings.HasPrefix("/"+p.Pattern, m+"/")
		}
		require.True(t, covered, "%s is outside every overlay mount, so the node agent cannot present it", p.Pattern)
	}
	for _, b := range sysfs.Bypasses {
		require.False(t, strings.HasPrefix(b.Pattern, "sys/"), "%s belongs in Paths", b.Pattern)
	}
}

func TestWriteInPlaceKeepsTheInode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "current_link_width")
	require.NoError(t, sysfs.WriteInPlace(path, []byte("16\n")))
	before := inode(t, path)

	require.NoError(t, sysfs.WriteInPlace(path, []byte("8\n")))
	require.Equal(t, before, inode(t, path), "an overlay keeps serving the old inode after a rename")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "8\n", string(got), "a shorter value must not leave a stale tail")
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	st, ok := fi.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	return st.Ino
}
