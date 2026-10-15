// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface

import corev1 "k8s.io/api/core/v1"

// PodSource is the read-only capability needed by a metrics consumer.
type PodSource interface {
	Pods() []*corev1.Pod
}

// PodCounts separates phase counts from independent termination and scheduling
// counts. A terminating Pod still contributes to its phase and Total.
type PodCounts struct {
	Total       int
	Pending     int
	Running     int
	Succeeded   int
	Failed      int
	Unknown     int
	Terminating int
	Unscheduled int
}

// CountPods includes completed, terminating, and unscheduled Pods. It performs
// no reads beyond the supplied observation and does not mutate its Pods.
func CountPods(source PodSource) PodCounts {
	var counts PodCounts
	for _, pod := range source.Pods() {
		counts.Total++
		switch pod.Status.Phase {
		case corev1.PodPending:
			counts.Pending++
		case corev1.PodRunning:
			counts.Running++
		case corev1.PodSucceeded:
			counts.Succeeded++
		case corev1.PodFailed:
			counts.Failed++
		default:
			counts.Unknown++
		}
		if pod.DeletionTimestamp != nil {
			counts.Terminating++
		}
		if pod.Spec.NodeName == "" {
			counts.Unscheduled++
		}
	}
	return counts
}
