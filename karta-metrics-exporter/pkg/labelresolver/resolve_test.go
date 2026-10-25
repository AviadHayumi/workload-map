// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package labelresolver

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelevaluator"
)

type countingCompiler struct {
	labelevaluator.Compiler
	calls       int
	evaluations atomic.Int64
}

func (c *countingCompiler) Compile(cfg labelevaluator.Config) (labelevaluator.Evaluator, error) {
	c.calls++
	evaluator, err := c.Compiler.Compile(cfg)
	if err != nil {
		return nil, err
	}
	return countedEvaluator{Evaluator: evaluator, calls: &c.evaluations}, nil
}

type countedEvaluator struct {
	labelevaluator.Evaluator
	calls *atomic.Int64
}

func (e countedEvaluator) Evaluate(ctx context.Context, object *unstructured.Unstructured) (string, error) {
	e.calls.Add(1)
	return e.Evaluator.Evaluate(ctx, object)
}

var _ = Describe("Resolver", func() {
	var cfg Config
	var input Input
	var compiler *countingCompiler
	BeforeEach(func() {
		compiler = &countingCompiler{Compiler: labelevaluator.NewCompiler()}
		cfg = Config{Rules: []LabelRule{{Name: "team", Metrics: []string{"karta_pod_workload_info"}, Sources: []Source{SourceWorkload, SourcePod}, Expression: `object.spec.team`}}}
		input = Input{Metrics: []string{"karta_pod_workload_info"}, Workload: &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"team": "root-team"}}}, Pod: &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"team": "pod-team"}}}}
	})
	It("compiles once, preserves source order and freezes configuration", func() {
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		Expect(compiler.calls).To(Equal(1))
		cfg.Rules[0].Sources[0] = SourcePod
		cfg.Rules[0].Metrics[0] = "mutated"
		for i := 0; i < 2; i++ {
			result, err := r.Resolve(context.Background(), input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result[0].Labels).To(HaveKeyWithValue("team", "root-team"))
			Expect(result[0].Errors).To(BeEmpty())
		}
		Expect(compiler.calls).To(Equal(1))
	})
	It("uses a full Pod object first when configured and a full workload object for status", func() {
		cfg.Rules[0].Sources = []Source{SourcePod, SourceWorkload}
		cfg.Rules[0].Expression = `object.kind + "/" + object.spec.team`
		cfg.Rules = append(cfg.Rules, LabelRule{Name: "team", Metrics: []string{"karta_workload_status"}, Sources: []Source{SourceWorkload}, Expression: `object.kind + "/" + object.spec.team`})
		input.Workload.SetKind("Job")
		input.Pod.SetKind("Pod")
		input.Metrics = append(input.Metrics, "karta_workload_status")
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels["team"]).To(Equal("Pod/pod-team"))
		Expect(result[1].Labels["team"]).To(Equal("Job/root-team"))
	})
	It("falls back after errors and retains them even when a later source succeeds", func() {
		input.Workload.Object = map[string]any{"spec": map[string]any{"team": int64(3)}}
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels).To(HaveKeyWithValue("team", "pod-team"))
		Expect(result[0].Errors["team"]).To(MatchError(ContainSubstring("must produce a string")))
		input.Workload = nil
		result, err = r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels["team"]).To(Equal("pod-team"))
		Expect(result[0].Errors["team"]).To(MatchError(ContainSubstring("input object is nil")))
	})
	It("falls back on empty values and never reuses previous labels", func() {
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		first, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		input.Workload.Object["spec"] = map[string]any{"team": ""}
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels["team"]).To(Equal("pod-team"))
		Expect(result[0].Errors).To(BeEmpty())
		input.Pod.Object["spec"] = map[string]any{"team": ""}
		result, err = r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels).To(BeEmpty())
		Expect(result[0].Errors).To(BeEmpty())
		Expect(first[0].Labels["team"]).To(Equal("root-team"))
		input.Pod = nil
		input.Workload = nil
		result, err = r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels).To(BeEmpty())
		Expect(result[0].Errors["team"]).To(MatchError(And(ContainSubstring("workload:"), ContainSubstring("pod:"))))
	})
	It("compiles a rule once for multiple metrics and accepts no rules", func() {
		cfg.Rules[0].Sources = []Source{SourceWorkload}
		cfg.Rules[0].Metrics = append(cfg.Rules[0].Metrics, "karta_workload_status")
		_, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		Expect(compiler.calls).To(Equal(1))
		r, err := New(Config{}, Dependencies{})
		Expect(err).NotTo(HaveOccurred())
		result, err := r.Resolve(context.Background(), Input{Metrics: input.Metrics})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveLen(1))
		Expect(result[0].Labels).To(BeEmpty())
		Expect(result[0].Errors).To(BeEmpty())
	})
	It("rejects configuration before compiling and rejects invalid expressions and limits", func() {
		bad := cfg
		bad.Rules = []LabelRule{{Name: "namespace"}}
		_, err := New(bad, Dependencies{Compiler: compiler})
		Expect(err).To(HaveOccurred())
		Expect(compiler.calls).To(BeZero())
		_, err = New(cfg, Dependencies{})
		Expect(err).To(HaveOccurred())
		cfg.Rules[0].Expression = "object["
		_, err = New(cfg, Dependencies{Compiler: compiler})
		Expect(err).To(HaveOccurred())
		_, err = New(Config{Limits: labelevaluator.Limits{MaxValueBytes: -1}}, Dependencies{})
		Expect(err).To(MatchError(labelevaluator.ErrNegativeLimit))
	})
	It("propagates limits to each compiled expression", func() {
		cfg.Limits.MaxValueBytes = 3
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels).To(BeEmpty())
		Expect(result[0].Errors["team"]).To(MatchError(ContainSubstring("maximum length")))
	})
	It("returns cancellation and invalid input as top-level errors", func() {
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = r.Resolve(ctx, input)
		Expect(err).To(MatchError(context.Canceled))
		input.Metrics = []string{"unknown"}
		_, err = r.Resolve(context.Background(), input)
		Expect(err).To(HaveOccurred())
		input.Metrics = []string{"karta_workload_status", "karta_workload_status"}
		_, err = r.Resolve(context.Background(), input)
		Expect(err).To(HaveOccurred())
	})
	It("retains a workload cost-limit error when Pod fallback succeeds", func() {
		expensive := fmt.Sprintf("string(size(%[1]s.map(a, %[1]s.map(b, %[1]s.map(c, a+b+c)))))", "[1,2,3,4,5,6,7,8,9,10]")
		cfg.Rules[0].Expression = `object.kind == "Pod" ? "fallback" : ` + expensive
		cfg.Limits.CostLimit = 30
		input.Workload.SetKind("Job")
		input.Pod.SetKind("Pod")
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels["team"]).To(Equal("fallback"))
		Expect(result[0].Errors["team"]).To(MatchError(ContainSubstring("cost limit")))
	})
	It("stops during a long workload evaluation without evaluating fallback", func() {
		input.Workload.SetUID("root")
		input.Workload.SetResourceVersion("1")
		cfg.Rules[0].Expression = fmt.Sprintf("string(size(%[1]s.map(a, %[1]s.map(b, %[1]s.map(c, %[1]s.map(d, %[1]s.map(e, %[1]s.map(f, a+b+c+d+e+f))))))))", "[1,2,3,4,5,6,7,8,9,10]")
		cfg.Limits.CostLimit = math.MaxUint64
		cfg.Limits.InterruptCheckFrequency = 1
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(5*time.Millisecond, cancel)
		defer timer.Stop()
		started := time.Now()
		result, err := r.Resolve(ctx, input)
		Expect(err).To(MatchError(context.Canceled))
		Expect(result).To(BeNil())
		Expect(r.(*resolver).workloadCache).To(BeEmpty())
		Expect(time.Since(started)).To(BeNumerically("<", time.Second))
	})

	It("reuses workload evaluations until resourceVersion changes", func() {
		input.Workload.SetUID("root")
		input.Workload.SetResourceVersion("1")
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < 10; i++ {
			result, err := r.Resolve(context.Background(), input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result[0].Labels["team"]).To(Equal("root-team"))
		}
		Expect(compiler.evaluations.Load()).To(Equal(int64(1)))
		input.Workload = input.Workload.DeepCopy()
		input.Workload.SetResourceVersion("2")
		input.Workload.Object["spec"] = map[string]any{"team": "new"}
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels["team"]).To(Equal("new"))
		Expect(compiler.evaluations.Load()).To(Equal(int64(2)))
	})
	It("does not cache objects without resource versions", func() {
		input.Workload.SetUID("root")
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		_, err = r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		input.Workload.Object["spec"] = map[string]any{"team": "new"}
		result, err := r.Resolve(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		Expect(result[0].Labels["team"]).To(Equal("new"))
		Expect(compiler.evaluations.Load()).To(Equal(int64(2)))
	})
	It("retains cached workload errors while evaluating each Pod fallback", func() {
		input.Workload.SetUID("root")
		input.Workload.SetResourceVersion("1")
		input.Workload.Object["spec"] = map[string]any{"team": int64(2)}
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		for _, team := range []string{"first", "second"} {
			input.Pod.Object["spec"] = map[string]any{"team": team}
			result, err := r.Resolve(context.Background(), input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result[0].Labels["team"]).To(Equal(team))
			Expect(result[0].Errors["team"]).To(MatchError(ContainSubstring("must produce a string")))
		}
		Expect(compiler.evaluations.Load()).To(Equal(int64(3)))
	})
	It("bounds the workload cache with FIFO eviction", func() {
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < workloadCacheSize+2; i++ {
			object := input.Workload.DeepCopy()
			object.SetUID(types.UID(fmt.Sprint(i)))
			object.SetResourceVersion("1")
			input.Workload = object
			_, err := r.Resolve(context.Background(), input)
			Expect(err).NotTo(HaveOccurred())
		}
		cache := r.(*resolver)
		Expect(cache.workloadCache).To(HaveLen(workloadCacheSize))
		Expect(cache.order).To(HaveLen(workloadCacheSize))
		Expect(cache.workloadCache).NotTo(HaveKey(workloadKey{uid: "0", version: "1", rule: 0}))
		Expect(cache.workloadCache).To(HaveKey(workloadKey{uid: "1025", version: "1", rule: 0}))
	})

	It("supports concurrent resolution without mutating objects", func() {
		input.Workload.SetUID("root")
		input.Workload.SetResourceVersion("1")
		r, err := New(cfg, Dependencies{Compiler: compiler})
		Expect(err).NotTo(HaveOccurred())
		before := input.Workload.DeepCopy()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				for j := 0; j < 50; j++ {
					result, err := r.Resolve(context.Background(), input)
					Expect(err).NotTo(HaveOccurred())
					Expect(result[0].Labels["team"]).To(Equal("root-team"))
				}
			}()
		}
		wg.Wait()
		Expect(input.Workload).To(Equal(before))
		Expect(compiler.calls).To(Equal(1))
	})
})
