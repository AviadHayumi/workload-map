// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package workloadinterface_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

type countingDiscoverer struct {
	karta.Discoverer
	calls int
}

type operatorReader struct {
	*datasetReader
	lists int
}

func ExamplePodReconciler() {
	ctx := context.Background()
	dataset, _ := jobSetDataset()
	reader := &datasetReader{dataset: dataset}
	sdk, err := karta.NewDiscoverer(reader, discovery.Options{MaxDepth: 8})
	if err != nil {
		panic(err)
	}

	// The consumer's projection stores one record per workload UID.
	workloads := make(map[types.UID]*discovery.Workload)
	podRoots := make(map[types.NamespacedName]discovery.ObjectRef)
	refreshRoots := workqueue.NewTyped[discovery.ObjectRef]()
	defer refreshRoots.ShutDown()
	reconciler := workloadinterface.PodReconciler{
		Discoverer: sdk, GetPod: currentPod(reader),
		Save: func(_ context.Context, key types.NamespacedName, observed *discovery.Workload) error {
			if previous, exists := podRoots[key]; exists && previous != observed.Root() {
				refreshRoots.Add(previous)
			}
			podRoots[key] = observed.Root()
			workloads[observed.Root().UID] = observed // One worker in this example.
			return nil
		},
		Remove: func(_ context.Context, key types.NamespacedName) error {
			if previous, exists := podRoots[key]; exists {
				refreshRoots.Add(previous)
				delete(podRoots, key)
			}
			return nil
		},
	}

	queue := workqueue.NewTyped[types.NamespacedName]()
	defer queue.ShutDown()
	enqueue := func(object any) {
		name, err := cache.DeletionHandlingObjectToName(object)
		if err != nil {
			panic(err)
		}
		queue.Add(types.NamespacedName{Namespace: name.Namespace, Name: name.Name})
	}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: enqueue, UpdateFunc: func(_, current any) { enqueue(current) }, DeleteFunc: enqueue,
	}
	// Register handler on the consumer's Pod informer. Start workers only after
	// its own handler and the discovery reader's complete scope are synchronized.
	// These calls simulate two delivered informer events.
	handler.OnAdd(dataset.Pods[0], false)
	handler.OnAdd(dataset.Pods[1], false)
	for range 2 {
		key, _ := queue.Get()
		err := reconciler.Reconcile(ctx, key)
		queue.Done(key)
		if err != nil {
			panic(err) // A production worker retries through a rate-limiting queue.
		}
	}
	fmt.Printf("Pod events=2 workload records=%d\n", len(workloads))
	for _, observed := range workloads {
		fmt.Printf("workload=%s/%s generic=%t pods=%v\n",
			observed.Root().GVK.Kind, observed.Root().Name, observed.Generic(), podNames(observed.Pods()))
		memberships := observed.PodMemberships()
		for _, pod := range observed.Pods() {
			fmt.Printf("  %s component=%s\n", pod.Name, memberships[pod.UID].Component)
		}
	}

	// A tombstone also enqueues a key. Reconcile observes NotFound, never calls
	// WorkloadForPod for that deleted Pod, and refreshes the saved former root.
	deleted := reader.dataset.Pods[0]
	reader.dataset.Pods = reader.dataset.Pods[1:]
	handler.OnDelete(cache.DeletedFinalStateUnknown{Key: deleted.Namespace + "/" + deleted.Name, Obj: deleted})
	key, _ := queue.Get()
	err = reconciler.Reconcile(ctx, key)
	queue.Done(key)
	if err != nil {
		panic(err)
	}
	previous, _ := refreshRoots.Get()
	updated, err := sdk.Workload(ctx, previous)
	refreshRoots.Done(previous)
	if err != nil {
		panic(err) // If the root is also gone, the consumer removes its projection.
	}
	workloads[previous.UID] = updated
	fmt.Printf("after deletion: workload records=%d pods=%v\n", len(workloads), podNames(updated.Pods()))
	// Output:
	// Pod events=2 workload records=1
	// workload=JobSet/training-set generic=false pods=[evaluation-0-pod training-0-pod training-1-pod]
	//   evaluation-0-pod component=replicatedjob
	//   training-0-pod component=replicatedjob
	//   training-1-pod component=replicatedjob
	// after deletion: workload records=1 pods=[evaluation-0-pod training-1-pod]
}

func ExamplePodReconciler_ownershipOnly() {
	ctx := context.Background()
	dataset, _ := jobSetDataset()
	reader := &operatorReader{datasetReader: &datasetReader{dataset: dataset}}
	sdk, err := karta.NewDiscoverer(reader, discovery.Options{MaxDepth: 8})
	if err != nil {
		panic(err)
	}
	// An existing ownership controller already read this Pod during reconcile.
	path, err := sdk.OwnershipPath(ctx, discovery.PodReference(dataset.Pods[0]))
	if err != nil {
		panic(err)
	}
	// The SDK returns Pod -> Job -> JobSet. The consumer chooses the presentation.
	slices.Reverse(path)
	for i, ref := range path {
		fmt.Printf("depth=%d %s/%s\n", i, ref.GVK.Kind, ref.Name)
	}
	fmt.Printf("Pod lists=%d\n", reader.lists)
	// Output:
	// depth=0 JobSet/training-set
	// depth=1 Job/training-0
	// depth=2 Pod/training-0-pod
	// Pod lists=0
}

func ExamplePodReconciler_definitionUpdates() {
	ctx := context.Background()
	dataset, root := jobSetDataset()
	selected, err := catalog.Get(root.GroupVersionKind())
	if err != nil {
		panic(err)
	}
	registry, err := discovery.NewDefinitionRegistry(selected)
	if err != nil {
		panic(err)
	}
	sdk, err := karta.NewDiscoverer(&datasetReader{dataset: dataset}, discovery.Options{DefinitionProvider: registry})
	if err != nil {
		panic(err)
	}
	// In an operator, definition watch callbacks enqueue one reconciliation.
	// That worker lists the current selected definitions from its synchronized
	// source, calls Replace, then requeues affected workloads after success.
	// These lists represent three successive reconciliations of that source.
	updates := []struct {
		name        string
		definitions []*v1alpha1.Karta
	}{
		{"selected", []*v1alpha1.Karta{selected}},
		{"removed", nil},
		{"restored", []*v1alpha1.Karta{selected}},
	}
	var first *discovery.Workload
	for _, update := range updates {
		if err := registry.Replace(update.definitions...); err != nil {
			panic(err) // Retry the update; the previous valid selection remains.
		}
		observed, err := sdk.WorkloadForPod(ctx, discovery.PodReference(dataset.Pods[0]))
		if err != nil {
			panic(err)
		}
		if first == nil {
			first = observed
		}
		component := observed.PodMemberships()[dataset.Pods[0].UID].Component
		fmt.Printf("%s: generic=%t component=%s pods=%d\n", update.name, observed.Generic(), component, len(observed.Pods()))
	}
	fmt.Printf("first observation remains generic=%t\n", first.Generic())
	// Output:
	// selected: generic=false component=replicatedjob pods=3
	// removed: generic=true component=pods pods=3
	// restored: generic=false component=replicatedjob pods=3
	// first observation remains generic=false
}

func TestPodReconcilerDefinedAndGeneric(t *testing.T) {
	for _, generic := range []bool{false, true} {
		t.Run(fmt.Sprintf("generic=%t", generic), func(t *testing.T) {
			root := jobObject()
			if generic {
				root.SetAPIVersion("example.org/v1")
				root.SetKind("Training")
			}
			pod := ownedPod("worker", root, corev1.PodRunning)
			reader := &datasetReader{dataset: jobDataset(root, []*corev1.Pod{pod})}
			sdk, err := karta.NewDiscoverer(reader, discovery.Options{})
			if err != nil {
				t.Fatal(err)
			}
			var saved *discovery.Workload
			reconciler := workloadinterface.PodReconciler{
				Discoverer: sdk, GetPod: currentPod(reader),
				Save: func(_ context.Context, _ types.NamespacedName, observed *discovery.Workload) error {
					saved = observed
					return nil
				},
				Remove: func(context.Context, types.NamespacedName) error { t.Fatal("unexpected removal"); return nil },
			}
			if err := reconciler.Reconcile(t.Context(), types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}); err != nil {
				t.Fatal(err)
			}
			if saved == nil || saved.Generic() != generic || len(saved.Pods()) != 1 {
				t.Fatalf("unexpected observation: %#v", saved)
			}
			if generic {
				if _, err := saved.Component("pods"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := saved.Component("job"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPodReconcilerOwnerChangeAndDeletion(t *testing.T) {
	root := jobObject()
	other := root.DeepCopy()
	other.SetName("other")
	other.SetUID("other-uid")
	pod := ownedPod("worker", root, corev1.PodRunning)
	dataset := jobDataset(root, []*corev1.Pod{pod})
	dataset.Objects = append(dataset.Objects, other)
	reader := &datasetReader{dataset: dataset}
	sdk, err := karta.NewDiscoverer(reader, discovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingDiscoverer{Discoverer: sdk}
	key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	associations := make(map[types.NamespacedName]discovery.ObjectRef)
	var refresh []discovery.ObjectRef
	reconciler := workloadinterface.PodReconciler{
		Discoverer: counted, GetPod: currentPod(reader),
		Save: func(_ context.Context, key types.NamespacedName, observed *discovery.Workload) error {
			if previous, exists := associations[key]; exists && previous != observed.Root() {
				refresh = append(refresh, previous)
			}
			associations[key] = observed.Root()
			return nil
		},
		Remove: func(_ context.Context, key types.NamespacedName) error {
			if previous, exists := associations[key]; exists {
				refresh = append(refresh, previous)
				delete(associations, key)
			}
			return nil
		},
	}
	if err := reconciler.Reconcile(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	// A new Pod with the same namespace/name supersedes a queued old event.
	replacement := ownedPod(pod.Name, other, corev1.PodPending)
	replacement.UID = "replacement-pod-uid"
	reader.dataset.Pods[0] = replacement
	if err := reconciler.Reconcile(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if associations[key] != objectRef(other) || len(refresh) != 1 || refresh[0] != objectRef(root) {
		t.Fatalf("root change lost a projection: associations=%v refresh=%v", associations, refresh)
	}
	reader.dataset.Pods = nil
	if err := reconciler.Reconcile(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if counted.calls != 2 || len(associations) != 0 || len(refresh) != 2 || refresh[1] != objectRef(other) {
		t.Fatalf("deletion: discovery calls=%d associations=%v refresh=%v", counted.calls, associations, refresh)
	}
	for _, ref := range refresh {
		observed, err := sdk.Workload(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if len(observed.Pods()) != 0 {
			t.Errorf("old workload %s retains Pods", ref.Name)
		}
	}
}

func TestPodReconcilerReadErrorLeavesProjection(t *testing.T) {
	readErr := errors.New("cache not synchronized")
	sdk := &countingDiscoverer{}
	reconciler := workloadinterface.PodReconciler{
		Discoverer: sdk,
		GetPod:     func(context.Context, types.NamespacedName) (*corev1.Pod, error) { return nil, readErr },
		Save: func(context.Context, types.NamespacedName, *discovery.Workload) error {
			t.Fatal("unexpected save")
			return nil
		},
		Remove: func(context.Context, types.NamespacedName) error { t.Fatal("unexpected removal"); return nil },
	}
	if err := reconciler.Reconcile(t.Context(), types.NamespacedName{Namespace: "example", Name: "worker"}); !errors.Is(err, readErr) {
		t.Fatalf("error = %v, want read error", err)
	}
	if sdk.calls != 0 {
		t.Fatal("discovered a Pod after its read failed")
	}
}

func TestPodReconcilerDiscoveryErrorLeavesProjection(t *testing.T) {
	dataset, _ := jobSetDataset()
	pod := dataset.Pods[0]
	// The Pod read succeeds, but its owner is missing from the acquired scope.
	dataset.Objects = nil
	reader := &datasetReader{dataset: dataset}
	sdk, err := karta.NewDiscoverer(reader, discovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingDiscoverer{Discoverer: sdk}
	reconciler := workloadinterface.PodReconciler{
		Discoverer: counted, GetPod: currentPod(reader),
		Save: func(context.Context, types.NamespacedName, *discovery.Workload) error {
			t.Fatal("incomplete discovery must not replace the saved projection")
			return nil
		},
		Remove: func(context.Context, types.NamespacedName) error {
			t.Fatal("owner lookup failure must not delete the saved association")
			return nil
		},
	}
	if err := reconciler.Reconcile(t.Context(), types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}); err == nil {
		t.Fatal("expected owner lookup failure")
	}
	if counted.calls != 1 {
		t.Fatalf("discovery calls = %d, want 1", counted.calls)
	}
}

func currentPod(reader *datasetReader) func(context.Context, types.NamespacedName) (*corev1.Pod, error) {
	return func(ctx context.Context, key types.NamespacedName) (*corev1.Pod, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, pod := range reader.dataset.Pods {
			if pod.Namespace == key.Namespace && pod.Name == key.Name {
				return pod.DeepCopy(), nil
			}
		}
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, key.Name)
	}
}

func (d *countingDiscoverer) WorkloadForPod(ctx context.Context, ref discovery.ObjectRef) (*discovery.Workload, error) {
	d.calls++
	return d.Discoverer.WorkloadForPod(ctx, ref)
}

func (r *operatorReader) ListPods(ctx context.Context, namespace string) (*corev1.PodList, error) {
	r.lists++
	return r.datasetReader.ListPods(ctx, namespace)
}
