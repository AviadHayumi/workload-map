// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func TestNearestReplicaSelectorDoesNotFallBack(t *testing.T) {
	for _, test := range []struct {
		name  string
		path  string
		group string
		err   error
	}{
		{name: "override", path: `"nearest"`, group: "nearest"},
		{name: "explicit_absence", path: `empty`},
		{name: "null_is_error", path: `null`, err: ErrEvaluation},
		{name: "multiple_is_error", path: `"a", "b"`, err: ErrEvaluation},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := kartas.LWS()
			for i := range definition.Spec.StructureDefinition.ChildComponents {
				component := &definition.Spec.StructureDefinition.ChildComponents[i]
				if component.Name == "worker" {
					component.PodSelector.ReplicaSelector = &v1alpha1.ReplicaSelector{KeyPath: test.path}
				}
			}
			d, w, pod, _ := evaluationFixture(t, definition, map[string]any{},
				map[string]string{"leaderworkerset.sigs.k8s.io/group-index": "2"},
				map[string]string{"leaderworkerset.sigs.k8s.io/leader-name": "leader"})
			mapping, err := w.PodMapping(t.Context(), pod, d.NewEvaluationBatch())
			if !errors.Is(err, test.err) || mapping.Group != test.group {
				t.Fatalf("mapping = %+v, err = %v, want group %q and error %v", mapping, err, test.group, test.err)
			}
		})
	}
}

func TestEmptyDeclaredInstanceListIsAnError(t *testing.T) {
	body := map[string]any{"spec": map[string]any{"replicatedJobs": []any{}}}
	d, w, pod, _ := evaluationFixture(t, kartas.Jobset(), body,
		map[string]string{"jobset.sigs.k8s.io/replicatedjob-name": "trainer", "jobset.sigs.k8s.io/job-index": "0"}, nil)
	if _, err := w.PodMapping(t.Context(), pod, d.NewEvaluationBatch()); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("empty declared instance list: %v", err)
	}
}

type readerFunc func(context.Context, ObjectRef) (*unstructured.Unstructured, error)

func (f readerFunc) Get(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
	return f(ctx, ref)
}

func TestRootChangeDuringPreparationIsStale(t *testing.T) {
	d, workload, _, originalReader := evaluationFixture(t, kartas.Jobset(), jobsetBody(), nil, nil)
	started, resume := make(chan struct{}), make(chan struct{})
	d.reader = readerFunc(func(ctx context.Context, ref ObjectRef) (*unstructured.Unstructured, error) {
		close(started)
		select {
		case <-resume:
			return originalReader.Get(ctx, ref)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	result := make(chan error, 1)
	go func() {
		_, err := workload.Status(t.Context(), d.NewEvaluationBatch())
		result <- err
	}()
	<-started
	updated := workload.root
	updated.ResourceVersion = "new"
	observe(d, updated)
	close(resume)
	if err := <-result; !errors.Is(err, ErrStale) {
		t.Fatalf("preparation accepted superseded root: %v", err)
	}
}
