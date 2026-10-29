// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestUpsertsCopyInputsAndPreserveCapturedViews(t *testing.T) {
	g := NewWithT(t)
	s := New(Options{})
	key := SourceKey{Resource: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespace: "ns"}
	root := ObjectRef{UID: "root", Namespace: "ns", Name: "job", GVK: schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}}
	pod := &PodResult{Record: PodRecord{UID: "pod", Workload: root, Karta: "original", KartaSource: "cluster", Labels: map[string]string{"team": "old"}}, HasValue: true, Sources: []SourceKey{key}}
	workload := &WorkloadResult{Record: WorkloadRecord{UID: root.UID, Ref: root, Phases: []string{"Running"}, StatusLabels: map[string]string{"team": "old"}}, HasValue: true, Sources: []SourceKey{key}, CreationSources: []SourceKey{key}}
	s.UpsertPod(pod)
	s.UpsertWorkload(workload)
	s.SetSourceHealth(key, SourceHealth{Synced: true})
	s.SetDiscoveryFailure(ObjectRef{UID: "candidate", Name: "original"}, ReasonUnresolvedOwner)
	old := s.Capture()
	pod.Record.Karta = "changed"
	pod.Record.Labels["team"] = "changed"
	pod.Sources[0].Namespace = "changed"
	workload.Record.Phases[0] = "Failed"
	workload.Record.StatusLabels["team"] = "changed"
	workload.Sources[0].Namespace = "changed"
	workload.CreationSources[0].Namespace = "changed"
	g.Expect(s.Pod("pod").Record.Karta).To(Equal("original"))
	g.Expect(s.Pod("pod").Record.Labels).To(HaveKeyWithValue("team", "old"))
	g.Expect(s.Pod("pod").Sources).To(Equal([]SourceKey{key}))
	g.Expect(s.Workload(root.UID).Record.Phases).To(Equal([]string{"Running"}))
	g.Expect(s.Workload(root.UID).Record.StatusLabels).To(HaveKeyWithValue("team", "old"))
	g.Expect(s.Workload(root.UID).Sources).To(Equal([]SourceKey{key}))
	g.Expect(s.Workload(root.UID).CreationSources).To(Equal([]SourceKey{key}))
	s.UpsertPod(pod)
	s.UpsertWorkload(workload)
	s.SetSourceHealth(key, SourceHealth{Error: ReasonPermissionDenied})
	s.SetDiscoveryFailure(ObjectRef{UID: "candidate", Name: "changed"}, ReasonAPIUnavailable)
	g.Expect(old.Pods[0].Record.Labels).To(HaveKeyWithValue("team", "old"))
	g.Expect(old.Workloads[0].Record.Phases).To(Equal([]string{"Running"}))
	g.Expect(old.Sources[key].Synced).To(BeTrue())
	g.Expect(old.Failures[0].Input.Name).To(Equal("original"))
	s.RemovePod("pod")
	s.RemoveWorkload(root.UID)
	s.RemoveSourceHealth(key)
	s.RemoveDiscoveryFailure("candidate")
	g.Expect(old.Pods).To(HaveLen(1))
	g.Expect(old.Workloads).To(HaveLen(1))
	g.Expect(old.Sources).To(HaveLen(1))
	g.Expect(old.Failures).To(HaveLen(1))
	g.Expect(s.Capture().Pods).To(BeEmpty())
	g.Expect(s.Capture().Workloads).To(BeEmpty())
	g.Expect(s.Capture().Sources).To(BeEmpty())
	g.Expect(s.Capture().Failures).To(BeEmpty())
}

func TestIndependentDomainsKeepEachOthersResultsAndErrors(t *testing.T) {
	g := NewWithT(t)
	s := New(Options{})
	root := ObjectRef{UID: "same-uid", Name: "standalone"}
	s.UpsertPod(&PodResult{Record: PodRecord{UID: root.UID, Workload: root}, HasValue: true, Error: ReasonEvaluationFailed})
	s.UpsertWorkload(&WorkloadResult{Record: WorkloadRecord{UID: root.UID, Ref: root, Phases: []string{"Running"}}, HasValue: true})
	delayedPod := *s.Pod(root.UID)
	s.UpsertWorkload(&WorkloadResult{Record: WorkloadRecord{UID: root.UID, Ref: root, Phases: []string{"Failed"}}, HasValue: true, StatusError: ReasonInvalidDefinition})
	s.UpsertPod(&delayedPod)
	g.Expect(s.Workload(root.UID).Record.Phases).To(Equal([]string{"Failed"}))
	g.Expect(s.Workload(root.UID).StatusError).To(Equal(ReasonInvalidDefinition))
	g.Expect(s.Pod(root.UID).Error).To(Equal(ReasonEvaluationFailed))
	s.SetDiscoveryFailure(root, ReasonUnresolvedOwner)
	s.RemoveDiscoveryFailure(root.UID)
	g.Expect(s.Workload(root.UID).HasValue).To(BeTrue())
	s.RemoveWorkload(root.UID)
	g.Expect(s.Pod(root.UID)).NotTo(BeNil())
	s.RemovePod(root.UID)
	g.Expect(s.Pod(root.UID)).To(BeNil())
	s.RemovePod("absent")
	s.RemoveWorkload("absent")
}

func TestValidityRequiresExactSourceAndPreservesObjectErrors(t *testing.T) {
	g := NewWithT(t)
	s := New(Options{Now: func() time.Time { panic("validity must not read the clock") }})
	key := SourceKey{Resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, Namespace: "ns"}
	sources := []SourceKey{key}
	valid, reason := s.Capture().Valid(sources, "")
	g.Expect(valid).To(BeFalse())
	g.Expect(reason).To(Equal(ReasonAPIUnavailable))
	s.SetSourceHealth(key, SourceHealth{})
	valid, _ = s.Capture().Valid(sources, "")
	g.Expect(valid).To(BeFalse())
	s.SetSourceHealth(key, SourceHealth{Synced: true, Error: ReasonPermissionDenied})
	valid, reason = s.Capture().Valid(sources, "")
	g.Expect(valid).To(BeFalse())
	g.Expect(reason).To(Equal(ReasonPermissionDenied))
	s.SetSourceHealth(key, SourceHealth{Synced: true})
	valid, reason = s.Capture().Valid(sources, ReasonEvaluationFailed)
	g.Expect(valid).To(BeFalse())
	g.Expect(reason).To(Equal(ReasonEvaluationFailed))
	valid, reason = s.Capture().Valid(sources, ReasonNone)
	g.Expect(valid).To(BeTrue())
	g.Expect(reason).To(Equal(ReasonNone))
	for _, different := range []SourceKey{
		{Resource: key.Resource},
		{Resource: key.Resource, Namespace: "other"},
		{Resource: schema.GroupVersionResource{Group: "batch", Version: "v2", Resource: "jobs"}, Namespace: "ns"},
		{Resource: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, Namespace: "ns"},
	} {
		valid, reason = s.Capture().Valid([]SourceKey{different}, "")
		g.Expect(valid).To(BeFalse())
		g.Expect(reason).To(Equal(ReasonAPIUnavailable))
	}
	s.RemoveSourceHealth(key)
	valid, _ = s.Capture().Valid(sources, "")
	g.Expect(valid).To(BeFalse())
}

func TestSourceErrorsHaveDeterministicPrecedence(t *testing.T) {
	g := NewWithT(t)
	denied := SourceKey{Namespace: "denied"}
	missing := SourceKey{Namespace: "missing"}
	view := CapturedView{Sources: map[SourceKey]SourceHealth{denied: {Error: ReasonPermissionDenied}}}
	for _, sources := range [][]SourceKey{{denied, missing}, {missing, denied}} {
		valid, reason := view.Valid(sources, "")
		g.Expect(valid).To(BeFalse())
		g.Expect(reason).To(Equal(ReasonPermissionDenied))
	}
}

func TestConcurrentUpsertsAndCapturesKeepDomainOwnership(t *testing.T) {
	s := New(Options{})
	var wg sync.WaitGroup
	for writer := 0; writer < 8; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			uid := types.UID(fmt.Sprint(writer))
			key := SourceKey{Namespace: string(uid)}
			for revision := 0; revision < 200; revision++ {
				value := fmt.Sprint(revision)
				if writer%2 == 0 {
					s.UpsertPod(&PodResult{Record: PodRecord{UID: uid, Name: value, Labels: map[string]string{"revision": value}}, Error: ReasonEvaluationFailed, Sources: []SourceKey{key}})
					s.Pod(uid)
				} else {
					s.UpsertWorkload(&WorkloadResult{Record: WorkloadRecord{UID: uid, Ref: ObjectRef{Name: value}, Phases: []string{value}}, Sources: []SourceKey{key}})
					s.Workload(uid)
				}
				s.SetSourceHealth(key, SourceHealth{Synced: true})
				s.SetDiscoveryFailure(ObjectRef{UID: uid}, ReasonUnresolvedOwner)
				view := s.Capture()
				for _, result := range view.Pods {
					if result.Record.Name != result.Record.Labels["revision"] || result.Error != ReasonEvaluationFailed {
						t.Error("capture observed a partial Pod update")
					}
					view.Valid(result.Sources, result.Error)
				}
				for _, result := range view.Workloads {
					if result.Record.Ref.Name != result.Record.Phases[0] {
						t.Error("capture observed a partial workload update")
					}
				}
				s.RemoveSourceHealth(key)
				s.RemoveDiscoveryFailure(uid)
			}
		}(writer)
	}
	wg.Wait()
	g := NewWithT(t)
	g.Expect(s.Capture().Pods).To(HaveLen(4))
	g.Expect(s.Capture().Workloads).To(HaveLen(4))
	g.Expect(s.Capture().Sources).To(BeEmpty())
	g.Expect(s.Capture().Failures).To(BeEmpty())
}
