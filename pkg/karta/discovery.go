// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package karta

import (
	"context"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

// Discoverer acquires immutable observations through a caller-owned reader.
// Each acquisition captures the reader's root object and complete Pod membership.
// Freshness and cache synchronization remain the reader owner's responsibility.
// Offline changes made through Workload do not change an acquired observation.
type Discoverer interface {
	Workload(context.Context, discovery.ObjectRef) (*discovery.Workload, error)
	WorkloadForPod(context.Context, discovery.ObjectRef) (*discovery.Workload, error)
}

// NewDiscoverer validates discovery options without accessing Kubernetes.
// Pass a Reader for direct acquisition or a populated discovery.Index for
// indexed queries. Options.Cache optionally reuses attribution on that Index.
// The caller owns index updates; construction starts no watches.
// With caching disabled, a nil reader is allowed; acquisition then returns
// discovery.ErrNoClient.
func NewDiscoverer(reader discovery.Reader, options discovery.Options) (Discoverer, error) {
	return discovery.New(reader, options)
}
