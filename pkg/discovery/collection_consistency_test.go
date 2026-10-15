// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
)

func TestCollectionRejectsPodOwnerAbsentFromCompleteList(t *testing.T) {
	for _, acquisition := range []string{"direct", "collect"} {
		t.Run(acquisition, func(t *testing.T) {
			parent := sdkPod("parent", nil, nil)
			child := sdkPod("child", nil, nil)
			child.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: parent.Name, UID: parent.UID, Controller: ptr.To(true)}}
			reader := sdkNewReader(t, nil, parent, child)
			// GET still finds the owner, but the complete Pod observation omits it.
			reader.pods = []*corev1.Pod{child}
			var err error
			switch acquisition {
			case "direct":
				resolver, constructionErr := discovery.New(reader, discovery.Options{})
				if constructionErr != nil {
					t.Fatal(constructionErr)
				}
				var workload *discovery.Workload
				workload, err = resolver.WorkloadForPod(context.Background(), sdkPodRef(child))
				if workload != nil {
					t.Errorf("returned partial workload rooted at %s with %d Pods", workload.Root().Name, len(workload.Pods()))
				}
			case "collect":
				_, err = discovery.Collect(context.Background(), reader, "team")
			}
			if !errors.Is(err, discovery.ErrStaleInput) {
				t.Fatalf("missing listed owner Pod: got %v, want ErrStaleInput", err)
			}
		})
	}
}

func TestTypedPodEmptyOwnerReferencesEqualAbsentReferences(t *testing.T) {
	for _, acquisition := range []string{"direct", "captured"} {
		t.Run(acquisition, func(t *testing.T) {
			pod := sdkPod("standalone", nil, nil)
			pod.OwnerReferences = []metav1.OwnerReference{}
			reader := sdkNewReader(t, nil, pod)
			var input discovery.Reader = reader
			if acquisition == "captured" {
				input = &sdkCapturedReader{sdkReader: reader, capture: &discovery.Capture{
					Seed: sdkPodRef(pod), Pods: []*corev1.Pod{pod}, Mappings: reader.mappings,
					Observation: discovery.Observation{Source: "indexed", Namespace: "team"},
				}}
			}
			resolver, err := discovery.New(input, discovery.Options{})
			if err != nil {
				t.Fatal(err)
			}
			workload, err := resolver.WorkloadForPod(context.Background(), sdkPodRef(pod))
			if err != nil {
				t.Fatalf("valid empty owner-reference slice rejected: %v", err)
			}
			if workload.Root().UID != pod.UID || len(workload.Pods()) != 1 {
				t.Fatal("standalone Pod observation is incomplete")
			}
		})
	}
}
