// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package execution

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"time"
	"weak"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Compiled program reuse", func() {
	BeforeEach(func() {
		programs = programCache{}
	})

	DescribeTable("does not let a mutable result change later runners",
		func(expression string, mutate func(any)) {
			ctx := context.Background()
			first, err := NewDefaultRunner(nil).Evaluate(ctx, expression)
			Expect(err).NotTo(HaveOccurred())
			expected, err := NewDefaultRunner(nil).Evaluate(ctx, expression)
			Expect(err).NotTo(HaveOccurred())
			mutate(first[0])
			actual, err := NewDefaultRunner(nil).Evaluate(ctx, expression)
			Expect(err).NotTo(HaveOccurred())
			Expect(actual).To(Equal(expected))
			Expect(programs.take(expression)).To(BeNil())
		},
		Entry("constant object", `{value: 1}`, func(v any) { v.(map[string]any)["value"] = 9 }),
		Entry("constant array", `[1, 2]`, func(v any) { v.([]any)[0] = 9 }),
		Entry("nested constant", `{nested: [{value: 1}]}`, func(v any) {
			v.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = 9
		}),
		Entry("large integer", `123456789012345678901234567890`, func(v any) { v.(*big.Int).SetInt64(9) }),
	)

	It("discards a reused program when a later call exposes a mutable constant", func() {
		expression := "if . then {value: 1} else 42 end"
		_, err := NewDefaultRunner(false).Evaluate(context.Background(), expression)
		Expect(err).NotTo(HaveOccurred())
		program := programs.take(expression)
		Expect(program).NotTo(BeNil())
		programs.put(expression, program)
		result, err := NewDefaultRunner(true).Evaluate(context.Background(), expression)
		Expect(err).NotTo(HaveOccurred())
		result[0].(map[string]any)["value"] = 9
		Expect(programs.take(expression)).To(BeNil())
		fresh, err := NewDefaultRunner(true).Evaluate(context.Background(), expression)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(Equal([]any{map[string]any{"value": 1}}))
		runtime.KeepAlive(program)
	})

	It("preserves result aliases within the original runner", func() {
		r := NewDefaultRunner(map[string]any{"value": 1})
		first, err := r.Evaluate(context.Background(), ".")
		Expect(err).NotTo(HaveOccurred())
		first[0].(map[string]any)["value"] = 9
		object, err := r.GetObject()
		Expect(err).NotTo(HaveOccurred())
		Expect(object.(map[string]any)["value"]).To(Equal(9))
		second, err := r.Evaluate(context.Background(), ".value")
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(Equal([]any{9}))
		other, err := NewDefaultRunner(map[string]any{"value": 1}).Evaluate(context.Background(), ".value")
		Expect(err).NotTo(HaveOccurred())
		Expect(other).To(Equal([]any{float64(1)}))
	})

	It("reuses scalar code with fresh inputs and execution contexts", func() {
		ctx := context.Background()
		first, err := NewDefaultRunner(1).Evaluate(ctx, ". + 1")
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(Equal([]any{float64(2)}))
		program := programs.take(". + 1")
		Expect(program).NotTo(BeNil())
		programs.put(". + 1", program)
		second, err := NewDefaultRunner(5).Evaluate(ctx, ". + 1")
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(Equal([]any{float64(6)}))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = NewDefaultRunner(1).Evaluate(cancelled, ". + 1")
		Expect(err).To(BeAssignableToTypeOf(&JQExecutionError{}))
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(programs.take(". + 1")).To(BeNil())
		third, err := NewDefaultRunner(9).Evaluate(ctx, ". + 1")
		Expect(err).NotTo(HaveOccurred())
		Expect(third).To(Equal([]any{float64(10)}))
		runtime.KeepAlive(program)
	})

	It("evaluates now against the current time on every call", func() {
		_, err := NewDefaultRunner(nil).Evaluate(context.Background(), "now")
		Expect(err).NotTo(HaveOccurred())
		program := programs.take("now")
		Expect(program).NotTo(BeNil())
		programs.put("now", program)
		before := float64(time.Now().UnixNano()) / 1e9
		result, err := NewDefaultRunner(nil).Evaluate(context.Background(), "now")
		Expect(err).NotTo(HaveOccurred())
		after := float64(time.Now().UnixNano()) / 1e9
		Expect(result[0]).To(BeNumerically(">=", before))
		Expect(result[0]).To(BeNumerically("<=", after))
		runtime.KeepAlive(program)
	})

	It("discards a program after its finite use budget", func() {
		_, err := NewDefaultRunner(nil).Evaluate(context.Background(), "42")
		Expect(err).NotTo(HaveOccurred())
		program := programs.idle["42"].Value.(idleProgram).program.Value()
		Expect(program).NotTo(BeNil())
		for range maxProgramUses - 1 {
			_, err = NewDefaultRunner(nil).Evaluate(context.Background(), "42")
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(program.uses).To(Equal(maxProgramUses))
		Expect(programs.take("42")).To(BeNil())
		_, err = NewDefaultRunner(nil).Evaluate(context.Background(), "42")
		Expect(err).NotTo(HaveOccurred())
		next := programs.take("42")
		Expect(next).NotTo(BeIdenticalTo(program))
		runtime.KeepAlive(program)
	})

	It("keeps a bounded LRU of expression keys", func() {
		cache := &programCache{}
		program := &compiledProgram{uses: 1}
		for i := range maxIdlePrograms {
			cache.put(fmt.Sprint(i), program)
		}
		cache.put("0", cache.take("0"))
		cache.put("extra", program)
		Expect(cache.idle).To(HaveLen(maxIdlePrograms))
		Expect(cache.order.Len()).To(Equal(maxIdlePrograms))
		Expect(cache.take("1")).To(BeNil())
		Expect(cache.take("0")).To(BeIdenticalTo(program))
		Expect(cache.take("extra")).To(BeIdenticalTo(program))
		runtime.KeepAlive(program)
	})

	It("releases idle compiler state to garbage collection", func() {
		cache := &programCache{}
		pointer := seedWeakProgram(cache)
		for range 3 {
			runtime.GC()
		}
		Expect(pointer.Value()).To(BeNil())
		Expect(cache.take("42")).To(BeNil())
		Expect(cache.idle).To(BeEmpty())
		Expect(cache.order.Len()).To(BeZero())
	})

	It("gives overlapping callers exclusive programs and keeps one returned idle program", func() {
		cache := &programCache{}
		first := &compiledProgram{uses: 1}
		second := &compiledProgram{uses: 1}
		cache.put("42", first)
		borrowed := cache.take("42")
		Expect(borrowed).To(BeIdenticalTo(first))
		Expect(cache.take("42")).To(BeNil())
		cache.put("42", second)
		cache.put("42", borrowed)
		Expect(cache.take("42")).To(BeIdenticalTo(second))
		runtime.KeepAlive(first)
		runtime.KeepAlive(second)
	})

	It("isolates concurrent inputs and mutable constants", func() {
		var group sync.WaitGroup
		errors := make(chan error, 16)
		for i := range 16 {
			group.Go(func() {
				for range 64 {
					result, err := NewDefaultRunner(i).Evaluate(context.Background(), ". + 1")
					if err != nil || len(result) != 1 || result[0] != float64(i+1) {
						errors <- fmt.Errorf("scalar input %d: %v, %w", i, result, err)
						return
					}
					result, err = NewDefaultRunner(nil).Evaluate(context.Background(), "{value: 1}")
					if err != nil || result[0].(map[string]any)["value"] != 1 {
						errors <- fmt.Errorf("mutable input %d: %v, %w", i, result, err)
						return
					}
					result[0].(map[string]any)["value"] = i
				}
			})
		}
		group.Wait()
		close(errors)
		for err := range errors {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	DescribeTable("preserves typed errors without caching failed evaluations", func(expression string, expected error) {
		_, err := NewDefaultRunner(nil).Evaluate(context.Background(), expression)
		Expect(err).To(BeAssignableToTypeOf(expected))
		Expect(programs.take(expression)).To(BeNil())
	},
		Entry("parse", "[", &JQParseError{}),
		Entry("compile", "$missing", &JQCompileError{}),
		Entry("execute", `error("failure")`, &JQExecutionError{}),
	)

	It("discards partial scalar output on result limits and timeouts", func() {
		limit := 2
		_, err := NewRunner(nil, &limit, nil).Evaluate(context.Background(), "range(10)")
		Expect(err).To(MatchError(ContainSubstring("exceed")))
		Expect(programs.take("range(10)")).To(BeNil())
		timeout := 1
		_, err = NewRunner(nil, nil, &timeout).Evaluate(context.Background(), "def forever: forever; forever")
		Expect(err).To(BeAssignableToTypeOf(&JQExecutionError{}))
		Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
		Expect(programs.take("def forever: forever; forever")).To(BeNil())
	})

	It("leaves variable binding and assignment behavior separate", func() {
		r := NewDefaultRunner(map[string]any{"value": 1, "items": []any{1, 2}})
		Expect(r.Assign(context.Background(), ".value", 7)).To(Succeed())
		result, err := r.Evaluate(context.Background(), ".value")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]any{float64(7)}))
		Expect(r.AssignZip(context.Background(), ".items[]", []any{3, 4})).To(Succeed())
		result, err = r.Evaluate(context.Background(), ".items[]")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]any{float64(3), float64(4)}))
		Expect(programs.idle).To(HaveLen(2))
	})

	It("allows only immutable scalar output types", func() {
		Expect(hasOnlyScalarResults([]any{nil, false, 1, 2.0, "value"})).To(BeTrue())
		Expect(hasOnlyScalarResults(nil)).To(BeTrue())
		Expect(hasOnlyScalarResults([]any{int64(1)})).To(BeFalse())
		Expect(hasOnlyScalarResults([]any{struct{}{}})).To(BeFalse())
		Expect(hasOnlyScalarResults([]any{1, []any{2}})).To(BeFalse())
	})
})

// Keep the allocation frame out of the GC assertion, so the weak cache is the
// only owner after this call returns.
//
//go:noinline
func seedWeakProgram(cache *programCache) weak.Pointer[compiledProgram] {
	code, err := (&runner{}).compile("42", nil)
	if err != nil {
		panic(err)
	}
	program := &compiledProgram{code: code, uses: 1}
	cache.put("42", program)
	return weak.Make(program)
}
