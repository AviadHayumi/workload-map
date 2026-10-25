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
	It("retires failure keys only after all publication and health references disappear", func() {
		s.SetPublicationFailure("root", ReasonPermissionDenied)
		one := &Publication{Workload: WorkloadRecord{UID: "one"}, Status: evidence}
		two := &Publication{Workload: WorkloadRecord{UID: "two"}, PodPages: []*PodPage{{Rows: []PublishedPod{{Mapping: evidence}}}}}
		s.Publish(one)
		s.Publish(two)
		s.SetPublicationHealth([]HealthRecord{{Evidence: evidence}})
		captured := s.Capture()
		next := *one
		s.Publish(&next)
		valid, _ := s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		s.RemovePublication("one")
		s.RemovePublication("two")
		valid, _ = s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		s.SetPublicationHealth(nil)
		concrete := s.(*Store)
		Expect(concrete.failures).To(BeEmpty())
		Expect(concrete.failureRefs).To(BeEmpty())
		valid, reason := captured.Valid(evidence)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(ReasonPermissionDenied))
	})
	It("retains references on shared Pod pages across single-Pod replacement", func() {
		shared := &PodPage{Rows: []PublishedPod{{Record: PodRecord{UID: "shared"}, Mapping: evidence}}}
		changed := &PodPage{Rows: []PublishedPod{{Record: PodRecord{UID: "changed"}, Mapping: evidence}}}
		p := &Publication{Workload: WorkloadRecord{UID: "root"}, PodPages: []*PodPage{shared, changed}}
		s.Publish(p)
		s.SetPublicationFailure("root", ReasonPermissionDenied)
		row := changed.Rows[0]
		row.Record.Labels = map[string]string{"team": "new"}
		s.Publish(p.WithPod("changed", &row))
		concrete := s.(*Store)
		Expect(concrete.failureRefs["root"]).To(Equal(2))
		valid, _ := s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		s.RemovePublication("root")
		Expect(concrete.failureRefs).To(BeEmpty())
		Expect(concrete.failures).To(BeEmpty())
	})

	It("keeps a failing stream invalid across removal and republication while health holds it", func() {
		s.SetPublicationHealth([]HealthRecord{{Evidence: evidence}})
		s.SetPublicationFailure("root", ReasonPermissionDenied)
		p := &Publication{Workload: WorkloadRecord{UID: "one"}, Status: evidence}
		s.Publish(p)
		s.RemovePublication("one")
		next := *p
		next.Status.ExpiresAt = now.Add(time.Hour)
		s.Publish(&next)
		valid, reason := s.Capture().Valid(next.Status)
		Expect(valid).To(BeFalse())
		Expect(reason).To(Equal(ReasonPermissionDenied))
		s.SetPublicationFailure("root", ReasonNone)
		Expect(s.(*Store).failures).To(BeEmpty())
	})

	It("retires abandoned keys without clearing referenced publication or health failures", func() {
		concrete := s.(*Store)
		s.SetPublicationFailure("abandoned", ReasonAPIUnavailable)
		concrete.RetirePublicationFailure("abandoned")
		Expect(concrete.failures).NotTo(HaveKey("abandoned"))
		s.SetPublicationFailure("root", ReasonPermissionDenied)
		s.Publish(&Publication{Workload: WorkloadRecord{UID: "root"}, Status: evidence})
		concrete.RetirePublicationFailure("root")
		valid, _ := s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		s.SetPublicationHealth([]HealthRecord{{Evidence: evidence}})
		s.RemovePublication("root")
		concrete.RetirePublicationFailure("root")
		valid, _ = s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		s.SetPublicationHealth(nil)
		concrete.RetirePublicationFailure("root")
		Expect(concrete.failures).To(BeEmpty())
	})

	It("keeps a health-only failure through replacement then retires it", func() {
		s.SetPublicationFailure("root", ReasonAPIUnavailable)
		s.SetPublicationHealth([]HealthRecord{{Evidence: evidence}})
		s.SetPublicationHealth([]HealthRecord{{Namespace: "new", Evidence: evidence}})
		valid, _ := s.Capture().Valid(evidence)
		Expect(valid).To(BeFalse())
		s.SetPublicationHealth(nil)
		Expect(s.(*Store).failures).To(BeEmpty())
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
	It("keeps sorted pages bounded through inserts, replacements and deletion", func() {
		rows := make([]PublishedPod, PodPageSize*2)
		for i := range rows {
			rows[i].Record.UID = types.UID(fmt.Sprintf("%04d", i*2))
		}
		p := &Publication{PodPages: BuildPodPages(rows, nil)}
		original := p
		for i := 0; i < 40; i++ {
			row := PublishedPod{Record: PodRecord{UID: types.UID(fmt.Sprintf("%04d", i*2+1))}}
			p = p.WithPod(row.Record.UID, &row)
		}
		for i := 0; i < 40; i++ {
			uid := types.UID(fmt.Sprintf("%04d", i*2+1))
			row, ok := p.Pod(uid)
			Expect(ok).To(BeTrue())
			Expect(row.Record.UID).To(Equal(uid))
			p = p.WithPod(uid, nil)
		}
		previous := types.UID("")
		count := 0
		for _, page := range p.PodPages {
			Expect(len(page.Rows)).To(BeNumerically("<=", PodPageSize))
			for _, row := range page.Rows {
				Expect(row.Record.UID > previous).To(BeTrue())
				previous = row.Record.UID
				count++
			}
		}
		Expect(count).To(Equal(len(rows)))
		Expect(original.PodPages).To(HaveLen(2))
		_, ok := original.Pod("0001")
		Expect(ok).To(BeFalse())
	})
	It("supports caller-provided unsorted pages", func() {
		p := &Publication{PodPages: []*PodPage{{Rows: []PublishedPod{{Record: PodRecord{UID: "z"}}, {Record: PodRecord{UID: "a"}}}}}}
		row, ok := p.Pod("a")
		Expect(ok).To(BeTrue())
		row.Record.Name = "new"
		next := p.WithPod("a", &row)
		result, ok := next.Pod("a")
		Expect(ok).To(BeTrue())
		Expect(result.Record.Name).To(Equal("new"))
		original, _ := p.Pod("a")
		Expect(original.Record.Name).To(BeEmpty())
	})
	It("reuses equal evidence instants regardless of time representation", func() {
		instant := time.Now()
		rows := []PublishedPod{{Record: PodRecord{UID: "one"}, Mapping: Evidence{ExpiresAt: instant}}}
		p := &Publication{PodPages: BuildPodPages(rows, nil)}
		rows[0].Mapping.ExpiresAt = instant.Round(0).UTC()
		Expect(BuildPodPages(rows, p)[0]).To(BeIdenticalTo(p.PodPages[0]))
	})

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

func BenchmarkPublishOnePodChange(b *testing.B) {
	for _, count := range []int{1000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := New(Options{})
			rows := make([]PublishedPod, count)
			evidence := Evidence{Dependencies: []string{"stream"}, ExpiresAt: time.Now().Add(time.Hour)}
			for i := range rows {
				rows[i] = PublishedPod{Record: PodRecord{UID: types.UID(fmt.Sprint(i))}, Mapping: evidence}
			}
			original := &Publication{Workload: WorkloadRecord{UID: "root"}, PodPages: BuildPodPages(rows, nil)}
			replacement := rows[0]
			replacement.Record.Labels = map[string]string{"team": "new"}
			changed := original.WithPod(replacement.Record.UID, &replacement)
			s.Publish(original)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if i%2 == 0 {
					s.Publish(changed)
				} else {
					s.Publish(original)
				}
			}
		})
	}
}
