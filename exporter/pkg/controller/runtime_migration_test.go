// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"slices"
	"testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRuntimeMissingCreationTimestampStaysAbsent(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "valid definition"
		if invalid {
			name = "invalid definition"
		}
		t.Run(name, func(t *testing.T) {
			job := runtimeJob("work", "root")
			job.SetCreationTimestamp(metav1.Time{})
			objects := []*unstructured.Unstructured{job, runtimePod("worker", "pod", job)}
			if invalid {
				objects = append(objects, invariantKarta("broken", "(", job.GroupVersionKind()))
			}
			fixture := startConnected(t, objects, func(options *RuntimeOptions) {
				options.Definitions.Cluster.Enabled = invalid
			}, "")
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, job.GetUID())
				if record == nil {
					return false
				}
				creation, _ := view.Valid(record.Creation)
				status, _ := view.Valid(record.Status)
				if invalid {
					return creation && slices.Contains(record.FailureReasons, store.ReasonInvalidDefinition)
				}
				return creation && status
			})
			_, record := invariantPublication(fixture, job.GetUID())
			if record.Workload.CreatedTimestamp != 0 {
				t.Fatalf("missing creation timestamp fabricated Unix seconds: %d", record.Workload.CreatedTimestamp)
			}
		})
	}
}
