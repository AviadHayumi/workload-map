// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package owner preserves the exporter's ownership API using the shared index.
package owner

import "github.com/dsx-ai-factory/workload-map/pkg/ownership"

type Ref = ownership.Ref
type WalkOutcome = ownership.WalkOutcome
type WalkResult = ownership.WalkResult
type Index = ownership.Index

const (
	OutcomeFound         = ownership.OutcomeFound
	OutcomeNoController  = ownership.OutcomeNoController
	OutcomeMissing       = ownership.OutcomeMissing
	OutcomeDepthExceeded = ownership.OutcomeDepthExceeded
)

func New() *Index {
	return ownership.New()
}
