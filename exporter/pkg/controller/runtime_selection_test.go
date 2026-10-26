// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	ktesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/submitterextractor"
)

type selectionGate struct {
	arrived chan watch.Event
	release chan struct{}
	once    sync.Once
}

type selectionWatchClient struct {
	dynamic.Interface
	mu      sync.Mutex
	gates   map[string]*selectionGate
	watches map[string]int
}

type selectionWatchResource struct {
	dynamic.NamespaceableResourceInterface
	client   *selectionWatchClient
	resource schema.GroupVersionResource
}

type selectionWatch struct {
	watch.Interface
	events  chan watch.Event
	stopped chan struct{}
	once    sync.Once
}

func (g *selectionGate) resume() { g.once.Do(func() { close(g.release) }) }

func (c *selectionWatchClient) pause(t *testing.T, consumer string) *selectionGate {
	t.Helper()
	gate := &selectionGate{arrived: make(chan watch.Event, 1), release: make(chan struct{})}
	c.mu.Lock()
	c.gates[consumer] = gate
	c.mu.Unlock()
	t.Cleanup(gate.resume)
	return gate
}

func (c *selectionWatchClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &selectionWatchResource{NamespaceableResourceInterface: c.Interface.Resource(resource), client: c, resource: resource}
}

func (c *selectionWatchClient) IsWatchListSemanticsUnSupported() bool { return true }

func (r *selectionWatchResource) Watch(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
	source, err := r.NamespaceableResourceInterface.Watch(ctx, options)
	if err != nil || r.resource != kartaGVR {
		return source, err
	}
	// The inventory starts an unbounded watch; the reflector adds its timeout.
	consumer := "inventory"
	if options.TimeoutSeconds != nil {
		consumer = "controller"
	}
	r.client.mu.Lock()
	r.client.watches[consumer]++
	r.client.mu.Unlock()
	proxy := &selectionWatch{Interface: source, events: make(chan watch.Event), stopped: make(chan struct{})}
	go func() {
		defer close(proxy.events)
		for {
			select {
			case <-ctx.Done():
				return
			case <-proxy.stopped:
				return
			case event, open := <-source.ResultChan():
				if !open {
					return
				}
				r.client.mu.Lock()
				gate := r.client.gates[consumer]
				r.client.mu.Unlock()
				if gate != nil {
					select {
					case gate.arrived <- event:
					default:
					}
					select {
					case <-gate.release:
					case <-ctx.Done():
						return
					case <-proxy.stopped:
						return
					}
				}
				select {
				case proxy.events <- event:
				case <-ctx.Done():
					return
				case <-proxy.stopped:
					return
				}
			}
		}
	}()
	return proxy, nil
}

func (w *selectionWatch) Stop()                          { w.once.Do(func() { close(w.stopped); w.Interface.Stop() }) }
func (w *selectionWatch) ResultChan() <-chan watch.Event { return w.events }

func awaitSelectionGate(t *testing.T, gate *selectionGate, definition *unstructured.Unstructured) {
	t.Helper()
	select {
	case event := <-gate.arrived:
		body, ok := event.Object.(*unstructured.Unstructured)
		if !ok || body.GetUID() != definition.GetUID() || body.GetResourceVersion() != definition.GetResourceVersion() {
			t.Fatalf("unexpected gated definition event: %+v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("definition event did not reach the independent watch gate")
	}
}

func startSelectionRuntime(t *testing.T, definitions ...*unstructured.Unstructured) (*connectedFixture, *selectionWatchClient, *unstructured.Unstructured) {
	t.Helper()
	job := runtimeJob("work", "root")
	objects := append([]*unstructured.Unstructured{job, runtimePod("worker", "pod", job)}, definitions...)
	var client *selectionWatchClient
	fixture := startConnected(t, objects, func(options *RuntimeOptions) { options.Definitions.Cluster.Enabled = true }, "",
		func(inner dynamic.Interface) dynamic.Interface {
			client = &selectionWatchClient{Interface: inner, gates: map[string]*selectionGate{}, watches: map[string]int{}}
			return client
		})
	awaitRuntime(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.watches["inventory"] == 1 && client.watches["controller"] == 1
	})
	return fixture, client, job
}

func assertSelectionWithdrawn(t *testing.T, fixture *connectedFixture) {
	t.Helper()
	_, record := invariantPublication(fixture, "root")
	if record == nil {
		return
	}
	if record.Workload.HasStatus || len(record.PodPages) != 0 || record.Workload.Karta != "" {
		t.Fatalf("superseded interpretation survived accepted winner change: %+v", record.Workload)
	}
}

func TestRuntimeSelectionIndependentCacheSkew(t *testing.T) {
	for _, lagging := range []string{"inventory", "controller"} {
		t.Run(lagging, func(t *testing.T) {
			definition := invariantKarta("before", "true", runtimeJob("unused", "unused").GroupVersionKind())
			fixture, client, job := startSelectionRuntime(t, definition)
			awaitInvariantMapping(t, fixture, "root", "pod", "before")
			gate := client.pause(t, lagging)
			changed := invariantKarta("after", "true", job.GroupVersionKind())
			changed.SetResourceVersion("2")
			if _, err := fixture.client.Resource(kartaGVR).Update(context.Background(), changed, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			awaitSelectionGate(t, gate, changed)
			awaitRuntime(t, func() bool {
				if lagging == "controller" {
					selection, err := fixture.controller.inventory.Lookup(job.GroupVersionKind())
					return err == nil && selection.Origin.ResourceVersion == "2"
				}
				fixture.controller.mu.Lock()
				defer fixture.controller.mu.Unlock()
				return fixture.controller.selections[job.GroupVersionKind().GroupKind()].Origin.ResourceVersion == "2"
			})
			if err := fixture.controller.reconcileRoot(context.Background(), "root"); !errors.Is(err, discovery.ErrStale) {
				t.Fatalf("cache skew returned %v, want stale", err)
			}
			if lagging == "inventory" {
				assertSelectionWithdrawn(t, fixture)
			} else {
				view, current := invariantPublication(fixture, "root")
				if current == nil {
					t.Fatal("inventory-only change removed known publication")
				}
				row, exists := current.Pod("pod")
				valid, _ := view.Valid(row.Mapping)
				if !exists || !valid || row.Record.Component != "before" || current.Workload.Karta != definition.GetName() {
					t.Fatal("inventory-only change published an unaccepted interpretation")
				}
			}
			gate.resume()
			awaitInvariantMapping(t, fixture, "root", "pod", "after")
		})
	}
}

func TestRuntimeSelectionNewCandidateRevokesSynchronously(t *testing.T) {
	definition := invariantKarta("before", "true", runtimeJob("unused", "unused").GroupVersionKind())
	fixture, client, job := startSelectionRuntime(t, definition)
	awaitInvariantMapping(t, fixture, "root", "pod", "before")
	gate := client.pause(t, "inventory")
	candidate := invariantKarta("older-winner", "true", job.GroupVersionKind())
	candidate.SetName("older-candidate")
	candidate.SetUID("older-candidate")
	candidate.SetCreationTimestamp(metav1.NewTime(time.Unix(50, 0)))
	if _, err := fixture.client.Resource(kartaGVR).Create(context.Background(), candidate, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitSelectionGate(t, gate, candidate)
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		return fixture.controller.selections[job.GroupVersionKind().GroupKind()].Origin.UID == candidate.GetUID()
	})
	assertSelectionWithdrawn(t, fixture)
	gate.resume()
	awaitInvariantMapping(t, fixture, "root", "pod", "older-winner")
}

func TestRuntimeSelectionLosingCandidateEditPreservesOutput(t *testing.T) {
	job := runtimeJob("unused", "unused")
	winner := invariantKarta("winner", "true", job.GroupVersionKind())
	loser := invariantKarta("loser", "true", job.GroupVersionKind())
	loser.SetName("loser")
	loser.SetUID("loser")
	loser.SetCreationTimestamp(metav1.NewTime(time.Unix(200, 0)))
	fixture, client, _ := startSelectionRuntime(t, winner, loser)
	awaitInvariantMapping(t, fixture, "root", "pod", "winner")
	gate := client.pause(t, "inventory")
	loser.SetResourceVersion("2")
	if err := unstructured.SetNestedField(loser.Object, "changed-loser", "spec", "structureDefinition", "rootComponent", "name"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.client.Resource(kartaGVR).Update(context.Background(), loser, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitSelectionGate(t, gate, loser)
	awaitRuntime(t, func() bool {
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		return fixture.controller.definitions[loser.GetUID()].ResourceVersion == "2"
	})
	view, record := invariantPublication(fixture, "root")
	if record == nil {
		t.Fatal("losing candidate removed workload")
	}
	row, exists := record.Pod("pod")
	mapping, _ := view.Valid(row.Mapping)
	status, _ := view.Valid(record.Status)
	if !exists || !mapping || !status || row.Record.Component != "winner" || record.Workload.Karta != winner.GetName() {
		t.Fatalf("losing candidate edit damaged winner output: %+v", record.Workload)
	}
	gate.resume()
	awaitInvariantMapping(t, fixture, "root", "pod", "winner")
}

func TestRuntimeSelectionReportedForbiddenIsNotPending(t *testing.T) {
	fixture, _, job := startSelectionRuntime(t)
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		valid, _ := view.Valid(record.Status)
		return valid
	})
	denied := apierrors.NewForbidden(invariantJobs.GroupResource(), "", errors.New("test reported denial"))
	fixture.client.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, denied })
	fixture.controller.failStream(invariantStream(t, fixture, invariantJobs), denied)
	_, err := fixture.controller.discovery.Resolve(context.Background(), objectReference(job, job.GroupVersionKind()))
	if !errors.Is(err, discovery.ErrPermissionDenied) || errors.Is(err, discovery.ErrPending) {
		t.Fatalf("reported403 resolved as %v, want permission denied", err)
	}
	view, record := invariantPublication(fixture, "root")
	if record == nil {
		t.Fatal("reported403 removed known workload")
	}
	if valid, reason := view.Valid(record.Status); valid || reason != store.ReasonPermissionDenied {
		t.Fatalf("status validity=%v reason=%s", valid, reason)
	}
}

type selectionReconcileKey struct{}

type selectionSubmitterBarrier struct {
	submitterextractor.Interface
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *selectionSubmitterBarrier) Evaluate(ctx context.Context, object metav1.Object, kind schema.GroupVersionKind) (submitterextractor.Submitter, error) {
	result, err := b.Interface.Evaluate(ctx, object, kind)
	if ctx.Value(selectionReconcileKey{}) == true && object.GetUID() == "root" {
		b.once.Do(func() { close(b.arrived) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return submitterextractor.Submitter{}, ctx.Err()
		}
	}
	return result, err
}

func TestRuntimeSelectionBlockedReconciliationRejectsChangedFacts(t *testing.T) {
	for _, change := range []string{"object", "definition", "scope", "watch"} {
		t.Run(change, func(t *testing.T) {
			job := runtimeJob("work", "root")
			definition := invariantKarta("before", "true", job.GroupVersionKind())
			var barrier *selectionSubmitterBarrier
			fixture := startConnectedWithHooks(t, []*unstructured.Unstructured{job, runtimePod("worker", "pod", job), definition}, func(options *RuntimeOptions) {
				options.Definitions.Cluster.Enabled = true
				options.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "team"}}
			}, `"alice"`, connectedHooks{WrapSubmitter: func(inner submitterextractor.Interface) submitterextractor.Interface {
				barrier = &selectionSubmitterBarrier{Interface: inner, arrived: make(chan struct{}), release: make(chan struct{})}
				return barrier
			}})
			awaitInvariantMapping(t, fixture, "root", "pod", "before")
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), selectionReconcileKey{}, true), 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- fixture.controller.reconcileRoot(ctx, "root") }()
			select {
			case <-barrier.arrived:
			case <-ctx.Done():
				t.Fatal("reconciliation did not reach evaluation barrier")
			}
			// The barrier is after status/mapping evaluation and before publication.
			switch change {
			case "object":
				job.SetResourceVersion("2")
				job.Object["status"] = map[string]any{"succeeded": int64(1)}
				if _, err := fixture.client.Resource(invariantJobs).Namespace("team").Update(context.Background(), job, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					return fixture.controller.reader.objects[refKey(objectReference(job, job.GroupVersionKind()))].observation.Ref.ResourceVersion == "2"
				})
			case "definition":
				definition.SetResourceVersion("2")
				if err := unstructured.SetNestedField(definition.Object, "after", "spec", "structureDefinition", "rootComponent", "name"); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.client.Resource(kartaGVR).Update(context.Background(), definition, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					return fixture.controller.selections[job.GroupVersionKind().GroupKind()].Origin.ResourceVersion == "2"
				})
			case "scope":
				namespaces := fixture.client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
				namespace, err := namespaces.Get(context.Background(), "team", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				namespace.SetLabels(nil)
				namespace.SetResourceVersion("2")
				if _, err = namespaces.Update(context.Background(), namespace, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				awaitRuntime(t, func() bool {
					fixture.controller.mu.Lock()
					defer fixture.controller.mu.Unlock()
					return !fixture.controller.namespaces["team"]
				})
			case "watch":
				denied := apierrors.NewForbidden(invariantJobs.GroupResource(), "", errors.New("test in-flight watch denial"))
				fixture.client.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, denied })
				fixture.controller.failStream(invariantStream(t, fixture, invariantJobs), denied)
			}
			close(barrier.release)
			select {
			case err := <-result:
				if !errors.Is(err, discovery.ErrStale) {
					t.Fatalf("reconciliation committed or misclassified after %s change: %v", change, err)
				}
			case <-ctx.Done():
				t.Fatal("blocked reconciliation did not finish")
			}
			if change == "scope" {
				_, record := invariantPublication(fixture, "root")
				if record != nil {
					t.Fatal("in-flight reconciliation republished excluded root")
				}
			}
		})
	}
}
