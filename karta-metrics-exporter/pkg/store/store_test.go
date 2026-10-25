// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
)

func TestStore(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Publication Store") }

var _ = Describe("Store", func() {
	var now time.Time
	var s Interface
	var evidence Evidence
	BeforeEach(func() {
		now = time.Unix(100, 0)
		s = New(Options{Now: func() time.Time { return now }})
		evidence = Evidence{ExpiresAt: now.Add(time.Minute), Dependencies: []string{"root"}}
	})
	It("captures time, explicit failures and dependencies independently of later writes", func() {
		s.SetPublicationFailure("root", ReasonPermissionDenied)
		p := &Publication{Workload: WorkloadRecord{UID: "root"}, Status: evidence}
		s.Publish(p)
		old := s.Capture()
		valid, reason := old.Valid(p.Status)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(ReasonPermissionDenied))
		s.SetPublicationFailure("root", "")
		valid, reason = s.Capture().Valid(evidence)
		Expect(valid).To(BeTrue())
		Expect(reason).To(Equal(ReasonNone))
		valid, _ = old.Valid(evidence)
		Expect(valid).To(BeFalse())
		fresh := s.Capture()
		now = evidence.ExpiresAt
		valid, _ = fresh.Valid(evidence)
		Expect(valid).To(BeTrue())
		valid, reason = s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(ReasonAPIUnavailable))
		explicit := evidence
		explicit.Reason = ReasonEvaluationFailed
		valid, reason = s.Capture().Valid(explicit)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(ReasonEvaluationFailed))
		valid, _ = fresh.Valid(Evidence{})
		Expect(valid).To(BeFalse())
	})
	It("retains old publications on replacement and removal and copies health inputs", func() {
		p := &Publication{Workload: WorkloadRecord{UID: "root", StatusLabels: map[string]string{"team": "old"}}}
		s.Publish(p)
		old := s.Capture()
		next := *p
		next.Workload.StatusLabels = map[string]string{"team": "new"}
		s.Publish(&next)
		Expect(s.Published("root")).To(BeIdenticalTo(&next))
		Expect(old.Records[0]).To(BeIdenticalTo(p))
		Expect(old.Records[0].Workload.StatusLabels).To(HaveKeyWithValue("team", "old"))
		health := []HealthRecord{{Namespace: "original", Evidence: evidence}}
		s.SetPublicationHealth(health)
		health[0].Namespace = "mutated"
		health[0].Evidence.Dependencies[0] = "mutated"
		h := s.Capture()
		Expect(h.Health[0].Namespace).To(Equal("original"))
		Expect(h.Health[0].Evidence.Dependencies).To(Equal([]string{"root"}))
		s.SetPublicationHealth(nil)
		Expect(h.Health).To(HaveLen(1))
		s.RemovePublication("root")
		s.RemovePublication("absent")
		Expect(s.Capture().Records).To(BeEmpty())
		Expect(old.Records).To(HaveLen(1))
		Expect(s.Published("root")).To(BeNil())
	})
	It("supports concurrent publication, capture, failure and health updates", func() {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				uid := types.UID(fmt.Sprint(i))
				for j := 0; j < 100; j++ {
					s.Publish(&Publication{Workload: WorkloadRecord{UID: uid}, Status: evidence})
					s.SetPublicationFailure(string(uid), ReasonAPIUnavailable)
					s.SetPublicationHealth([]HealthRecord{{Evidence: evidence}})
					s.Published(uid)
					view := s.Capture()
					for _, p := range view.Records {
						view.Valid(p.Status)
					}
					s.RemovePublication(uid)
					s.SetPublicationFailure(string(uid), "")
				}
			}(i)
		}
		wg.Wait()
		Expect(s.Capture().Records).To(BeEmpty())
	})
})

var _ = Describe("Pod pages", func() {
	It("copies only changed pages and never reorders the input", func() {
		rows := make([]PublishedPod, PodPageSize+1)
		for i := range rows {
			rows[i] = PublishedPod{Record: PodRecord{UID: types.UID(fmt.Sprintf("%04d", len(rows)-i))}}
		}
		first := rows[0].Record.UID
		p := &Publication{PodPages: BuildPodPages(rows, nil)}
		Expect(rows[0].Record.UID).To(Equal(first))
		Expect(p.PodPages).To(HaveLen(2))
		unchanged := BuildPodPages(rows, p)
		Expect(unchanged[0]).To(BeIdenticalTo(p.PodPages[0]))
		Expect(unchanged[1]).To(BeIdenticalTo(p.PodPages[1]))
		pod := p.PodPages[0].Rows[0]
		pod.Record.Labels = map[string]string{"team": "new"}
		next := p.WithPod(pod.Record.UID, &pod)
		Expect(next.PodPages[0]).NotTo(BeIdenticalTo(p.PodPages[0]))
		Expect(next.PodPages[1]).To(BeIdenticalTo(p.PodPages[1]))
		Expect(p.PodPages[0].Rows[0].Record.Labels).To(BeNil())
		found, ok := next.Pod(pod.Record.UID)
		Expect(ok).To(BeTrue())
		Expect(found.Record.Labels).To(HaveKeyWithValue("team", "new"))
		removed := next.WithPod(pod.Record.UID, nil)
		_, ok = removed.Pod(pod.Record.UID)
		Expect(ok).To(BeFalse())
		Expect(next.PodPages[0].Rows).To(HaveLen(PodPageSize))
		last := p.PodPages[1].Rows[0]
		empty := p.WithPod(last.Record.UID, nil)
		Expect(empty.PodPages).To(HaveLen(1))
		added := p.WithPod("new", &PublishedPod{Record: PodRecord{UID: "new"}})
		Expect(added.PodPages[0]).To(BeIdenticalTo(p.PodPages[0]))
		Expect(added.PodPages[1].Rows).To(HaveLen(2))
		full := &Publication{PodPages: p.PodPages[:1]}
		Expect(full.WithPod("new", &PublishedPod{Record: PodRecord{UID: "new"}}).PodPages).To(HaveLen(2))
		Expect(p.WithPod("absent", nil).PodPages[0]).To(BeIdenticalTo(p.PodPages[0]))
	})
})
