// Copyright 2026 Trainy, Inc.
// SPDX-License-Identifier: Apache-2.0

package overrides

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/NVIDIA/k8s-test-infra/internal/fsutil"
)

// DefaultTTL bounds how often a Store stats the file. The Wave 0 census
// counted ~900 handle lookups for one `amd-smi list`; within the TTL a lookup
// costs a mutex and a clock read, never a syscall or a parse.
const DefaultTTL = time.Second

// Store serves the current override document from a file, re-reading it only
// when its stat fingerprint changes. Writers replace the file by rename
// (Update), so the inode is part of the fingerprint: two writes landing in
// one mtime tick with the same size still read as a change.
type Store struct {
	path string
	ttl  time.Duration
	now  func() time.Time

	mu      sync.Mutex
	checked time.Time
	print   fingerprint
	gen     uint64
	doc     *Doc
	err     error
}

type fingerprint struct {
	present bool
	ino     uint64
	size    int64
	mtime   time.Time
}

// NewStore returns a store for path. ttl <= 0 stats on every call.
func NewStore(path string, ttl time.Duration) *Store {
	return &Store{path: path, ttl: ttl, now: time.Now, doc: &Doc{}}
}

// Snapshot returns the current document and its generation. The generation
// increases whenever the observed document changes, so callers cache work
// keyed on it. A document that fails to parse is reported in err and the last
// good document is kept: a half-edited file must not flip every GPU back to
// healthy, and must not bump the generation either.
func (s *Store) Snapshot() (gen uint64, doc *Doc, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if !s.checked.IsZero() && now.Sub(s.checked) < s.ttl {
		return s.gen, s.doc, s.err
	}
	s.checked = now

	fp, err := stat(s.path)
	if err != nil {
		s.err = err
		return s.gen, s.doc, s.err
	}
	if fp == s.print {
		s.err = nil // the file is back to the document already served
		return s.gen, s.doc, nil
	}
	next := &Doc{}
	if fp.present {
		data, err := os.ReadFile(s.path)
		if err != nil {
			s.err = fmt.Errorf("reading overrides: %w", err)
			return s.gen, s.doc, s.err
		}
		if next, err = Parse(data); err != nil {
			s.err = err
			return s.gen, s.doc, s.err
		}
	}
	s.print, s.doc, s.err = fp, next, nil
	s.gen++
	return s.gen, s.doc, nil
}

func stat(path string) (fingerprint, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fingerprint{}, nil
	}
	if err != nil {
		return fingerprint{}, fmt.Errorf("stat overrides: %w", err)
	}
	fp := fingerprint{present: true, size: fi.Size(), mtime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		fp.ino = st.Ino
	}
	return fp, nil
}

// Update applies fn to the document at path and writes the result back
// atomically (temp file + rename), holding an exclusive lock on a sibling
// lock file so concurrent writers (two amd-sim-ctl invocations, the node
// agent's DRM remap) never lose each other's change.
//
// The rename is safe here because the override file is never served through
// the sysfs overlay; files that are (package sysfs) must be written in place.
func Update(path string, fn func(*Doc) error) error {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening override lock: %w", err)
	}
	defer func() { _ = lock.Close() }() // closing releases the flock
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("locking overrides: %w", err)
	}

	doc := &Doc{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("reading overrides: %w", err)
	default:
		if doc, err = Parse(data); err != nil {
			return err
		}
	}
	if err := fn(doc); err != nil {
		return err
	}
	out, err := doc.Bytes()
	if err != nil {
		return err
	}
	if err := fsutil.Write(path, out, 0o644); err != nil {
		return fmt.Errorf("writing overrides: %w", err)
	}
	return nil
}
