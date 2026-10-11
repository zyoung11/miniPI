// SPDX-FileCopyrightText: Copyright Hewlett Packard Enterprise Development LP
// SPDX-License-Identifier: MIT

// Package profiling writes Go runtime profiles when PI_PROFILE names them.
//
// With PI_PROFILE unset, Start performs one environment lookup and returns a
// no-op, so release binaries pay nothing for the capability. With it set, pi
// profiles the whole process and writes the files on exit:
//
//	PI_PROFILE=cpu,heap pi -p "hello"
//	PI_PROFILE=trace PI_PROFILE_DIR=/tmp/pi-profile pi
//
// Kinds are cpu, heap, allocs, block, mutex, goroutine, and trace. Files go to
// PI_PROFILE_DIR (default: the current directory) as pi-<pid>-<kind>.pprof,
// or pi-<pid>.trace for the execution trace. Read them with `go tool pprof`
// and `go tool trace`, or run `make profile` from a source checkout.
package profiling

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strings"
	"sync"
)

// Kinds lists the accepted PI_PROFILE values in the order they are written.
var Kinds = []string{"cpu", "heap", "allocs", "block", "mutex", "goroutine", "trace"}

// Start begins the profiles named by PI_PROFILE and returns the function that
// stops them and writes every file. The returned function is safe to call more
// than once; only the first call writes.
func Start() (stop func()) {
	spec := os.Getenv("PI_PROFILE")
	if spec == "" {
		return func() {}
	}
	return StartWith(spec, os.Getenv("PI_PROFILE_DIR"), os.Stderr)
}

// StartWith is Start with explicit inputs. Problems are reported to diag and
// never stop pi: a profile that cannot be written is skipped.
func StartWith(spec, dir string, diag io.Writer) (stop func()) {
	if dir == "" {
		dir = "."
	}
	kinds := map[string]bool{}
	for kind := range strings.SplitSeq(spec, ",") {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if !slices.Contains(Kinds, kind) {
			warnf(diag, "pi: ignoring unknown PI_PROFILE kind %q (valid: %s)\n", kind, strings.Join(Kinds, ", "))
			continue
		}
		kinds[kind] = true
	}
	if len(kinds) == 0 {
		return func() {}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		warnf(diag, "pi: PI_PROFILE_DIR: %v\n", err)
		return func() {}
	}
	prefix := filepath.Join(dir, fmt.Sprintf("pi-%d", os.Getpid()))
	var cpuFile, traceFile *os.File
	if kinds["cpu"] {
		cpuFile = create(prefix+"-cpu.pprof", diag)
		if cpuFile != nil {
			if err := pprof.StartCPUProfile(cpuFile); err != nil {
				warnf(diag, "pi: cpu profile: %v\n", err)
				_ = cpuFile.Close() // The profile never started; the empty file carries nothing.
				cpuFile = nil
			}
		}
	}
	if kinds["trace"] {
		traceFile = create(prefix+".trace", diag)
		if traceFile != nil {
			if err := trace.Start(traceFile); err != nil {
				warnf(diag, "pi: trace: %v\n", err)
				_ = traceFile.Close() // The trace never started; the empty file carries nothing.
				traceFile = nil
			}
		}
	}
	if kinds["block"] {
		runtime.SetBlockProfileRate(1)
	}
	if kinds["mutex"] {
		runtime.SetMutexProfileFraction(1)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			var written []string
			if cpuFile != nil {
				pprof.StopCPUProfile()
				if err := cpuFile.Close(); err != nil {
					warnf(diag, "pi: cpu profile: %v\n", err)
				} else {
					written = append(written, cpuFile.Name())
				}
			}
			if traceFile != nil {
				trace.Stop()
				if err := traceFile.Close(); err != nil {
					warnf(diag, "pi: trace: %v\n", err)
				} else {
					written = append(written, traceFile.Name())
				}
			}
			for _, kind := range []string{"heap", "allocs", "block", "mutex", "goroutine"} {
				if !kinds[kind] {
					continue
				}
				if kind == "heap" {
					runtime.GC() // Report live objects as of exit, not the last GC cycle.
				}
				path := prefix + "-" + kind + ".pprof"
				if f := create(path, diag); f != nil {
					err := pprof.Lookup(kind).WriteTo(f, 0)
					if closeErr := f.Close(); err == nil {
						err = closeErr
					}
					if err != nil {
						warnf(diag, "pi: %s profile: %v\n", kind, err)
					} else {
						written = append(written, path)
					}
				}
			}
			for _, path := range written {
				warnf(diag, "pi: wrote %s\n", path)
			}
		})
	}
}

func create(path string, diag io.Writer) *os.File {
	f, err := os.Create(path)
	if err != nil {
		warnf(diag, "pi: %v\n", err)
		return nil
	}
	return f
}

// warnf reports a best-effort diagnostic. A failed diagnostic write has no
// better channel, so it is dropped.
func warnf(diag io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(diag, format, args...)
}
