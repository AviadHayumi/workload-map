// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build !darwin && !linux

package main

func peakRSS() (uint64, bool) { return 0, false }
