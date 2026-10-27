// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
)

const (
	podMetric    = "karta_pod_workload_info"
	statusMetric = "karta_workload_status"
)

var _ = Describe("Resolver", func() {
	var cfg Config
	var workload, pod *unstructured.Unstructured
	BeforeEach(func() {
		cfg = Config{Rules: []LabelRule{{Name: "team", Metrics: []string{podMetric}, Sources: []Source{SourceWorkload, SourcePod}, Expression: `object.spec.team`}}}
		workload = &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"team": "root-team"}}}
		pod = &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"team": "pod-team"}}}
	})
	It("preserves source order and freezes configuration", func() {
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		cfg.Rules[0].Sources[0] = SourcePod
		cfg.Rules[0].Metrics[0] = "mutated"
		resolve := r.ForWorkload(workload)
		for i := 0; i < 2; i++ {
			result, err := resolve(context.Background(), podMetric, pod)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Labels).To(HaveKeyWithValue("team", "root-team"))
			Expect(result.Errors).To(BeEmpty())
		}
	})
	It("uses full Pod and workload objects in the configured order", func() {
		cfg.Rules[0].Sources = []Source{SourcePod, SourceWorkload}
		cfg.Rules[0].Expression = `object.kind + "/" + object.spec.team`
		cfg.Rules = append(cfg.Rules, LabelRule{Name: "team", Metrics: []string{statusMetric}, Sources: []Source{SourceWorkload}, Expression: `object.kind + "/" + object.spec.team`})
		workload.SetKind("Job")
		pod.SetKind("Pod")
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		resolve := r.ForWorkload(workload)
		result, err := resolve(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("Pod/pod-team"))
		result, err = resolve(context.Background(), statusMetric, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("Job/root-team"))
	})
	It("retains workload errors while evaluating each Pod fallback", func() {
		workload.Object["spec"] = map[string]any{"team": int64(2)}
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		calls := countEvaluations(r)
		resolve := r.ForWorkload(workload)
		for _, team := range []string{"first", "second"} {
			pod.Object["spec"] = map[string]any{"team": team}
			result, err := resolve(context.Background(), podMetric, pod)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Labels["team"]).To(Equal(team))
			Expect(result.Errors["team"]).To(MatchError(ContainSubstring("must produce a string")))
		}
		Expect(calls.Load()).To(Equal(int64(3)))
		result, err := r.ForWorkload(nil)(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("second"))
		Expect(result.Errors["team"]).To(MatchError(ContainSubstring("input object is nil")))
	})
	It("falls back on empty values and returns independent result maps", func() {
		workload.Object["spec"] = map[string]any{"team": ""}
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		resolve := r.ForWorkload(workload)
		first, err := resolve(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		pod.Object["spec"] = map[string]any{"team": ""}
		result, err := resolve(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels).To(BeEmpty())
		Expect(result.Errors).To(BeEmpty())
		Expect(first.Labels["team"]).To(Equal("pod-team"))
		result.Labels["team"] = "changed"
		Expect(first.Labels["team"]).To(Equal("pod-team"))
		resolve = r.ForWorkload(nil)
		first, err = resolve(context.Background(), podMetric, nil)
		Expect(err).NotTo(HaveOccurred())
		result, err = resolve(context.Background(), podMetric, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels).To(BeEmpty())
		Expect(result.Errors["team"]).To(MatchError(And(ContainSubstring("workload:"), ContainSubstring("pod:"))))
		delete(result.Errors, "team")
		Expect(first.Errors).To(HaveKey("team"))
	})
	It("shares one compiled rule and one workload evaluation across metrics and 1000 Pods", func() {
		cfg.Rules[0].Sources = []Source{SourceWorkload}
		cfg.Rules[0].Metrics = []string{podMetric, statusMetric}
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.rules[podMetric][0]).To(BeIdenticalTo(r.rules[statusMetric][0]))
		calls := countEvaluations(r)
		resolve := r.ForWorkload(workload)
		for i := 0; i < 1000; i++ {
			pod.SetName(fmt.Sprint(i))
			result, err := resolve(context.Background(), podMetric, pod)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Labels["team"]).To(Equal("root-team"))
		}
		_, err = resolve(context.Background(), statusMetric, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls.Load()).To(Equal(int64(1)))
	})
	It("evaluates workload fallback only when needed", func() {
		cfg.Rules[0].Sources = []Source{SourcePod, SourceWorkload}
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		calls := countEvaluations(r)
		resolve := r.ForWorkload(workload)
		_, err = resolve(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls.Load()).To(Equal(int64(1)))
		pod.Object["spec"] = map[string]any{"team": ""}
		for i := 0; i < 2; i++ {
			result, err := resolve(context.Background(), podMetric, pod)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Labels["team"]).To(Equal("root-team"))
		}
		Expect(calls.Load()).To(Equal(int64(4)))
	})
	It("prepares fresh results for changed workloads and API representations", func() {
		cfg.Rules[0].Expression = `object.apiVersion + "/" + object.kind + "/" + object.spec.team`
		workload.SetAPIVersion("example.com/v1")
		workload.SetKind("Workload")
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		first := r.ForWorkload(workload)
		result, err := first(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("example.com/v1/Workload/root-team"))
		updated := workload.DeepCopy()
		updated.SetAPIVersion("example.com/v2")
		updated.SetKind("ConvertedWorkload")
		updated.Object["spec"] = map[string]any{"team": "new"}
		result, err = r.ForWorkload(updated)(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("example.com/v2/ConvertedWorkload/new"))
		result, err = first(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("example.com/v1/Workload/root-team"))
	})
	It("accepts no rules and rejects invalid expressions and limits", func() {
		r, err := New(Config{})
		Expect(err).NotTo(HaveOccurred())
		result, err := r.ForWorkload(nil)(context.Background(), podMetric, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels).To(BeEmpty())
		Expect(result.Errors).To(BeEmpty())
		_, err = New(Config{Rules: []LabelRule{{Name: "namespace"}}})
		Expect(err).To(HaveOccurred())
		cfg.Rules[0].Expression = "object["
		_, err = New(cfg)
		Expect(err).To(HaveOccurred())
		_, err = New(Config{Limits: labelevaluator.Limits{MaxValueBytes: -1}})
		Expect(err).To(MatchError(labelevaluator.ErrNegativeLimit))
	})
	It("propagates limits to each expression", func() {
		cfg.Limits.MaxValueBytes = 3
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		result, err := r.ForWorkload(workload)(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels).To(BeEmpty())
		Expect(result.Errors["team"]).To(MatchError(ContainSubstring("maximum length")))
	})
	It("returns cancellation and invalid metrics as top-level errors, including after caching", func() {
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		resolve := r.ForWorkload(workload)
		_, err = resolve(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = resolve(ctx, podMetric, pod)
		Expect(err).To(MatchError(context.Canceled))
		_, err = resolve(context.Background(), "unknown", pod)
		Expect(err).To(HaveOccurred())
	})
	It("does not cache cancellation returned by an evaluator", func() {
		for _, interruption := range []error{context.Canceled, context.DeadlineExceeded} {
			r, err := New(cfg)
			Expect(err).NotTo(HaveOccurred())
			rule := r.rules[podMetric][0]
			evaluate := rule.evaluate
			calls := 0
			rule.evaluate = func(ctx context.Context, object *unstructured.Unstructured) (string, error) {
				calls++
				if calls == 1 {
					return "", fmt.Errorf("interrupted: %w", interruption)
				}
				return evaluate(ctx, object)
			}
			resolve := r.ForWorkload(workload)
			_, err = resolve(context.Background(), podMetric, pod)
			Expect(err).To(MatchError(ContainSubstring(interruption.Error())))
			result, err := resolve(context.Background(), podMetric, pod)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Labels["team"]).To(Equal("root-team"))
			Expect(calls).To(Equal(2))
		}
	})
	It("retains a workload cost-limit error when Pod fallback succeeds", func() {
		expensive := fmt.Sprintf("string(size(%[1]s.map(a, %[1]s.map(b, %[1]s.map(c, a+b+c)))))", "[1,2,3,4,5,6,7,8,9,10]")
		cfg.Rules[0].Expression = `object.kind == "Pod" ? "fallback" : ` + expensive
		cfg.Limits.CostLimit = 30
		workload.SetKind("Job")
		pod.SetKind("Pod")
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		result, err := r.ForWorkload(workload)(context.Background(), podMetric, pod)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Labels["team"]).To(Equal("fallback"))
		Expect(result.Errors["team"]).To(MatchError(ContainSubstring("cost limit")))
	})
	It("stops during a long workload evaluation without evaluating fallback", func() {
		cfg.Rules[0].Expression = fmt.Sprintf("string(size(%[1]s.map(a, %[1]s.map(b, %[1]s.map(c, %[1]s.map(d, %[1]s.map(e, %[1]s.map(f, a+b+c+d+e+f))))))))", "[1,2,3,4,5,6,7,8,9,10]")
		cfg.Limits.CostLimit = math.MaxUint64
		cfg.Limits.InterruptCheckFrequency = 1
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		calls := countEvaluations(r)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(5*time.Millisecond, cancel)
		defer timer.Stop()
		started := time.Now()
		result, err := r.ForWorkload(workload)(ctx, podMetric, pod)
		Expect(err).To(MatchError(context.Canceled))
		Expect(result.Labels).To(BeNil())
		Expect(calls.Load()).To(Equal(int64(1)))
		Expect(time.Since(started)).To(BeNumerically("<", time.Second))
	})
	It("supports independent scopes concurrently without mutating workload objects", func() {
		r, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		calls := countEvaluations(r)
		before := workload.DeepCopy()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Go(func() {
				defer GinkgoRecover()
				resolve := r.ForWorkload(workload)
				for j := 0; j < 50; j++ {
					result, err := resolve(context.Background(), podMetric, pod)
					Expect(err).NotTo(HaveOccurred())
					Expect(result.Labels["team"]).To(Equal("root-team"))
				}
			})
		}
		wg.Wait()
		Expect(workload).To(Equal(before))
		Expect(calls.Load()).To(Equal(int64(8)))
	})
})

func countEvaluations(r *Resolver) *atomic.Int64 {
	calls := new(atomic.Int64)
	seen := make(map[*compiledRule]bool)
	for _, rules := range r.rules {
		for _, rule := range rules {
			if seen[rule] {
				continue
			}
			seen[rule] = true
			evaluate := rule.evaluate
			rule.evaluate = func(ctx context.Context, object *unstructured.Unstructured) (string, error) {
				calls.Add(1)
				return evaluate(ctx, object)
			}
		}
	}
	return calls
}

func BenchmarkWorkloadLabels(b *testing.B) {
	cfg := Config{Rules: []LabelRule{{
		Name: "team", Metrics: []string{podMetric}, Sources: []Source{SourceWorkload},
		Expression: `object.spec.teams.filter(t, t.active).map(t, t.name)[0]`,
	}}}
	teams := make([]any, 100)
	for i := range teams {
		teams[i] = map[string]any{"name": fmt.Sprint(i), "active": i%2 == 0}
	}
	workload := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"teams": teams}}}
	workload.SetAPIVersion("batch/v1")
	workload.SetKind("Job")
	workload.SetUID("root")
	workload.SetResourceVersion("1")
	pods := make([]*unstructured.Unstructured, 1000)
	for i := range pods {
		pods[i] = &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": fmt.Sprint(i)}}}
	}
	r, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	for _, workloadChanged := range []bool{false, true} {
		b.Run(fmt.Sprintf("1000Pods/workloadChanged=%t", workloadChanged), func(b *testing.B) {
			resolve := r.ForWorkload(workload)
			version := 1
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if workloadChanged {
					version++
					updated := workload.DeepCopy()
					updated.SetResourceVersion(fmt.Sprint(version))
					resolve = r.ForWorkload(updated)
				}
				for _, pod := range pods {
					result, err := resolve(context.Background(), podMetric, pod)
					if err != nil || len(result.Errors) != 0 || result.Labels["team"] == "" {
						b.Fatalf("resolve: %v, labels: %+v", err, result)
					}
				}
			}
		})
	}
}
