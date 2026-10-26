// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build darwin || linux

package main

import (
	"runtime"
	"syscall"
)

func peakRSS() (uint64, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	bytes := uint64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		bytes *= 1024
	}
	return bytes, true
}
