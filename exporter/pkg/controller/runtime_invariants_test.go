// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	ktesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

type invariantDeadlineClient struct {
	dynamic.Interface
	mode           atomic.Int64
	root           *unstructured.Unstructured
	requestTimeout time.Duration
}

type invariantDeadlineResource struct {
	dynamic.NamespaceableResourceInterface
	client   *invariantDeadlineClient
	resource schema.GroupVersionResource
}

type invariantDeadlineNamespaced struct {
	dynamic.ResourceInterface
	client   *invariantDeadlineClient
	resource schema.GroupVersionResource
}

var invariantJobs = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
var invariantPods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

func invariantPublication(fixture *connectedFixture, uid types.UID) (store.CapturedView, *store.Publication) {
	view := fixture.store.Capture()
	for _, record := range view.Records {
		if record.Workload.UID == uid {
			return view, record
		}
	}
	return view, nil
}

func awaitInvariantMapping(t *testing.T, fixture *connectedFixture, root, pod types.UID, component string) {
	t.Helper()
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, root)
		if record == nil {
			return false
		}
		row, exists := record.Pod(pod)
		valid, _ := view.Valid(row.Mapping)
		return exists && valid && row.Record.WorkloadUID == root && row.Record.Component == component
	})
}

func invariantStream(t *testing.T, fixture *connectedFixture, resource schema.GroupVersionResource) *resourceStream {
	t.Helper()
	fixture.controller.mu.Lock()
	defer fixture.controller.mu.Unlock()
	stream := fixture.controller.reader.streams[discovery.CoverageKey{Resource: resource, Namespace: "team"}]
	if stream == nil {
		t.Fatalf("missing accepted stream for %s", resource)
	}
	return stream
}

func TestRuntimeInvariantReportedWatchFailureAndRecovery(t *testing.T) {
	job := runtimeJob("work", "root")
	job.SetAnnotations(map[string]string{"submitter": "alice"})
	fixture := startConnected(t, []*unstructured.Unstructured{job, runtimePod("worker", "pod", job)}, func(options *RuntimeOptions) { options.RefreshInterval = 50 * time.Millisecond },
		`"submitter" in annotations ? annotations["submitter"] : ""`)
	invariantDebug(t, fixture)
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		row, exists := record.Pod("pod")
		mapping, _ := view.Valid(row.Mapping)
		status, _ := view.Valid(record.Status)
		membership, _ := view.Valid(record.Membership)
		return exists && mapping && status && membership && record.SubmittedBy == "alice"
	})
	before, old := invariantPublication(fixture, "root")
	var blocked atomic.Bool
	blocked.Store(true)
	fixture.client.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if !blocked.Load() {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(invariantJobs.GroupResource(), "", errors.New("test watch authorization loss"))
	})
	stream := invariantStream(t, fixture, invariantJobs)
	source := watch.NewRaceFreeFake()
	monitored := fixture.controller.monitorWatch(context.Background(), stream, source)
	defer monitored.Stop()
	source.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: 403})
	select {
	case event := <-monitored.ResultChan():
		if event.Type != watch.Error {
			t.Fatalf("event = %s, want Error", event.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch error was not forwarded")
	}
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		row, exists := record.Pod("pod")
		mapping, reason := view.Valid(row.Mapping)
		status, _ := view.Valid(record.Status)
		creation, _ := view.Valid(record.Creation)
		membership, _ := view.Valid(record.Membership)
		return exists && !mapping && reason == store.ReasonPermissionDenied && !status && !creation && !membership
	})
	if valid, _ := before.Valid(old.Status); !valid {
		t.Fatal("watch failure mutated an already captured view")
	}
	blocked.Store(false)
	fixture.controller.queue.Add(queuedWork{Operation: "verify", GVK: job.GroupVersionKind(), Namespace: "team"})
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		row, exists := record.Pod("pod")
		mapping, _ := view.Valid(row.Mapping)
		status, _ := view.Valid(record.Status)
		creation, _ := view.Valid(record.Creation)
		membership, _ := view.Valid(record.Membership)
		return exists && mapping && status && creation && membership && record.SubmittedBy == "alice"
	})
}

func TestRuntimeInvariantIncompleteFreshListDoesNotRenewProof(t *testing.T) {
	for _, failure := range []string{"second-page-error", "repeated-token", "changed-list-version"} {
		t.Run(failure, func(t *testing.T) {
			job := runtimeJob("work", "root")
			fixture := startConnected(t, []*unstructured.Unstructured{job}, func(options *RuntimeOptions) { options.PageSize = 1 }, "")
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, "root")
				if record == nil {
					return false
				}
				valid, _ := view.Valid(record.Status)
				return valid
			})
			stream := invariantStream(t, fixture, invariantJobs)
			fixture.controller.mu.Lock()
			before := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
			verifiedAt := stream.verifiedAt
			fixture.controller.mu.Unlock()
			var blocked atomic.Bool
			blocked.Store(true)
			var pages atomic.Int64
			fixture.client.PrependReactor("list", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
				if !blocked.Load() {
					return false, nil, nil
				}
				options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
				if options.ResourceVersion != "" || options.Limit != 1 || options.LabelSelector != "" || options.FieldSelector != "" {
					return true, nil, errors.New("verification LIST was not fresh, bounded and unfiltered")
				}
				pages.Add(1)
				page := &unstructured.UnstructuredList{}
				page.SetResourceVersion("100")
				if options.Continue == "" {
					page.Items = []unstructured.Unstructured{*job.DeepCopy()}
					page.SetContinue("next")
					return true, page, nil
				}
				switch failure {
				case "second-page-error":
					return true, nil, errors.New("test second page failure")
				case "repeated-token":
					page.SetContinue("next")
				case "changed-list-version":
					page.SetResourceVersion("101")
				}
				return true, page, nil
			})
			fixture.now.Add(int64(time.Minute))
			err := fixture.controller.verifyStream(context.Background(), queuedWork{Operation: "verify", GVK: job.GroupVersionKind(), Namespace: "team"})
			if err == nil {
				t.Fatal("incomplete LIST unexpectedly succeeded")
			}
			if pages.Load() < 2 {
				t.Fatalf("test did not reach the failing second page: %v", err)
			}
			fixture.controller.mu.Lock()
			after := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
			afterVerified := stream.verifiedAt
			fixture.controller.mu.Unlock()
			if after != before || !afterVerified.Equal(verifiedAt) {
				t.Fatalf("failed LIST renewed proof: before=%+v after=%+v", before, after)
			}
			view, record := invariantPublication(fixture, "root")
			if record == nil {
				t.Fatal("failure removed the known workload")
			}
			if valid, _ := view.Valid(record.Status); valid {
				t.Fatal("status stayed valid after failed verification")
			}
			blocked.Store(false)
			fixture.controller.queue.Add(queuedWork{Operation: "verify", GVK: job.GroupVersionKind(), Namespace: "team"})
			awaitRuntime(t, func() bool {
				view, record := invariantPublication(fixture, "root")
				if record == nil {
					return false
				}
				valid, _ := view.Valid(record.Status)
				fixture.controller.mu.Lock()
				renewed := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
				fixture.controller.mu.Unlock()
				return valid && renewed.expiresAt.After(before.expiresAt)
			})
		})
	}
}

func invariantKarta(component, expression string, root schema.GroupVersionKind) *unstructured.Unstructured {
	definition := runtimeObject(schema.GroupVersionKind{Group: "run.ai", Version: "v1alpha1", Kind: "Karta"}, "connected-definition", "definition")
	definition.SetNamespace("")
	definition.Object["spec"] = map[string]any{"structureDefinition": map[string]any{"rootComponent": map[string]any{
		"name":             component,
		"kind":             map[string]any{"group": root.Group, "version": root.Version, "kind": root.Kind},
		"specDefinition":   map[string]any{"podTemplateSpecPath": ".spec.template"},
		"statusDefinition": map[string]any{"statusMappings": map[string]any{"running": []any{map[string]any{"byExpression": map[string]any{"expression": expression, "expectedResult": "true"}}}}},
	}}}
	return definition
}

func TestRuntimeInvariantInitialInvalidKartaZeroPods(t *testing.T) {
	job := runtimeJob("zero", "root")
	definition := invariantKarta("broken", "(", job.GroupVersionKind())
	fixture := startConnected(t, []*unstructured.Unstructured{job, definition}, func(options *RuntimeOptions) {
		options.Definitions.Cluster.Enabled = true
	}, "")
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		creation, _ := view.Valid(record.Creation)
		return creation && !record.Workload.HasStatus && len(record.PodPages) == 0 && slices.Contains(record.FailureReasons, store.ReasonInvalidDefinition)
	})
}

func TestRuntimeInvariantKartaAddChangeDelete(t *testing.T) {
	root := runtimeJob("work", "root")
	root.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Training"})
	fixture := startConnected(t, []*unstructured.Unstructured{root, runtimePod("worker", "pod", root)}, func(options *RuntimeOptions) {
		options.Definitions.Catalog.Enabled = false
		options.Definitions.Cluster.Enabled = true
	}, "")
	awaitInvariantMapping(t, fixture, "root", "pod", "<unknown>")
	definitions := fixture.client.Resource(kartaGVR)
	definition := invariantKarta("first", "true", root.GroupVersionKind())
	created, err := definitions.Create(context.Background(), definition, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	awaitInvariantMapping(t, fixture, "root", "pod", "first")
	updated := invariantKarta("second", "true", root.GroupVersionKind())
	updated.SetResourceVersion("2")
	if _, err = definitions.Update(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitInvariantMapping(t, fixture, "root", "pod", "second")
	view, record := invariantPublication(fixture, "root")
	status, _ := view.Valid(record.Status)
	if !status || !record.Workload.HasStatus || record.Workload.Karta != created.GetName() || record.Workload.KartaSource != "cluster" {
		t.Fatalf("updated selected definition not reflected in status: %+v", record.Workload)
	}
	if err = definitions.Delete(context.Background(), definition.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitInvariantMapping(t, fixture, "root", "pod", "<unknown>")
	_, record = invariantPublication(fixture, "root")
	if record.Workload.HasStatus || record.Workload.Karta != "" || record.Workload.KartaSource != "" {
		t.Fatalf("deleted Karta interpretation survived: %+v", record.Workload)
	}
}

func TestRuntimeInvariantParentRecreationRejectsOldOwnerUID(t *testing.T) {
	old := runtimeJob("same", "old-root")
	pod := runtimePod("worker", "old-pod", old)
	fixture := startConnected(t, []*unstructured.Unstructured{old, pod}, nil, "")
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, old.GetUID())
		if record == nil {
			return false
		}
		row, exists := record.Pod(pod.GetUID())
		valid, _ := view.Valid(row.Mapping)
		return exists && valid
	})
	jobs := fixture.client.Resource(invariantJobs).Namespace("team")
	if err := jobs.Delete(context.Background(), old.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool { _, record := invariantPublication(fixture, old.GetUID()); return record == nil })
	replacement := runtimeJob(old.GetName(), "new-root")
	replacement.SetResourceVersion("2")
	if _, err := jobs.Create(context.Background(), replacement, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, replacement.GetUID())
		if record == nil {
			return false
		}
		status, _ := view.Valid(record.Status)
		return status && record.Workload.HasStatus
	})
	_, record := invariantPublication(fixture, replacement.GetUID())
	if _, exists := record.Pod(pod.GetUID()); exists {
		t.Fatal("old owner UID was attached to same-name replacement")
	}
	// Reparent the same live Pod with a new resourceVersion. Only this event may attach it.
	pod.SetOwnerReferences(runtimePod("unused", "unused", replacement).GetOwnerReferences())
	pod.SetResourceVersion("2")
	if _, err := fixture.client.Resource(invariantPods).Namespace("team").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, replacement.GetUID())
		if record == nil {
			return false
		}
		row, exists := record.Pod(pod.GetUID())
		valid, _ := view.Valid(row.Mapping)
		_, oldRecord := invariantPublication(fixture, old.GetUID())
		return exists && valid && row.Record.WorkloadUID == replacement.GetUID() && oldRecord == nil
	})
	// A delayed tombstone from the old UID must not remove the replacement.
	fixture.controller.acceptObject(invariantStream(t, fixture, invariantJobs), old, true)
	view, record := invariantPublication(fixture, replacement.GetUID())
	if record == nil {
		t.Fatal("old tombstone removed replacement")
	}
	if valid, _ := view.Valid(record.Status); !valid {
		t.Fatal("old tombstone invalidated replacement status")
	}
}

func TestRuntimeInvariantScopeAndHiddenPodLabelTransitions(t *testing.T) {
	job := runtimeJob("work", "root")
	job.SetLabels(map[string]string{"selected": "yes"})
	job.SetAnnotations(map[string]string{"submitter": "alice"})
	pod := runtimePod("worker", "pod", job)
	pod.SetLabels(map[string]string{"visible": "yes"})
	fixture := startConnected(t, []*unstructured.Unstructured{job, pod}, func(options *RuntimeOptions) {
		options.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "team"}}
		options.ObjectSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"selected": "yes"}}
		options.PodSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"visible": "yes"}}
	}, `"submitter" in annotations ? annotations["submitter"] : ""`)
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		row, exists := record.Pod("pod")
		valid, _ := view.Valid(row.Mapping)
		return exists && valid
	})
	pod.SetLabels(nil)
	pod.SetAnnotations(map[string]string{"submitter": "bob"})
	pod.SetResourceVersion("2")
	if _, err := fixture.client.Resource(invariantPods).Namespace("team").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		_, visible := record.Pod("pod")
		status, _ := view.Valid(record.Status)
		return !visible && status && record.SubmittedBy == ""
	})
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
		view := fixture.store.Capture()
		if len(view.Records) != 0 {
			return false
		}
		for _, health := range view.Health {
			if health.Ref.Namespace == "team" {
				return false
			}
		}
		return true
	})
	namespace.SetLabels(map[string]string{"kubernetes.io/metadata.name": "team"})
	namespace.SetResourceVersion("3")
	if _, err = namespaces.Update(context.Background(), namespace, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		valid, _ := view.Valid(record.Status)
		_, visible := record.Pod("pod")
		return valid && !visible && record.SubmittedBy == ""
	})
	job.SetLabels(nil)
	job.SetResourceVersion("2")
	if _, err = fixture.client.Resource(invariantJobs).Namespace("team").Update(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool { return len(fixture.store.Capture().Records) == 0 })
	job.SetLabels(map[string]string{"selected": "yes"})
	job.SetResourceVersion("3")
	if _, err = fixture.client.Resource(invariantJobs).Namespace("team").Update(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		valid, _ := view.Valid(record.Status)
		return valid
	})
}

func (c *invariantDeadlineClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &invariantDeadlineResource{NamespaceableResourceInterface: c.Interface.Resource(resource), client: c, resource: resource}
}

func (r *invariantDeadlineResource) Namespace(namespace string) dynamic.ResourceInterface {
	return &invariantDeadlineNamespaced{ResourceInterface: r.NamespaceableResourceInterface.Namespace(namespace), client: r.client, resource: r.resource}
}

func (r *invariantDeadlineNamespaced) List(ctx context.Context, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	mode := r.client.mode.Load()
	if mode == 0 || r.resource != invariantJobs {
		return r.ResourceInterface.List(ctx, options)
	}
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) > r.client.requestTimeout {
		return nil, errors.New("LIST page lacks its request deadline")
	}
	delay := r.client.requestTimeout / 2
	if mode == 2 {
		delay = 2 * r.client.requestTimeout
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	page := &unstructured.UnstructuredList{}
	page.SetResourceVersion("100")
	switch options.Continue {
	case "":
		page.Items = []unstructured.Unstructured{*r.client.root.DeepCopy()}
		page.SetContinue("second")
	case "second":
		page.SetContinue("third")
	case "third":
	default:
		return nil, errors.New("unexpected continuation")
	}
	return page, nil
}

func TestRuntimeInvariantFreshListUsesPerPageDeadlines(t *testing.T) {
	job := runtimeJob("slow", "root")
	const requestTimeout = 500 * time.Millisecond
	var client *invariantDeadlineClient
	fixture := startConnected(t, []*unstructured.Unstructured{job}, func(options *RuntimeOptions) {
		options.RequestTimeout = requestTimeout
		options.PageSize = 1
	}, "", func(inner dynamic.Interface) dynamic.Interface {
		client = &invariantDeadlineClient{Interface: inner, root: job, requestTimeout: requestTimeout}
		return client
	})
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		valid, _ := view.Valid(record.Status)
		return valid
	})
	client.mode.Store(1)
	fixture.now.Add(int64(time.Minute))
	started := time.Now()
	key := queuedWork{Operation: "verify", GVK: job.GroupVersionKind(), Namespace: "team"}
	if err := fixture.controller.verifyStream(context.Background(), key); err != nil {
		t.Fatalf("three individually bounded pages failed: %v", err)
	}
	if elapsed := time.Since(started); elapsed <= requestTimeout {
		t.Fatalf("chain lasted %s; test did not exceed request timeout %s", elapsed, requestTimeout)
	}
	stream := invariantStream(t, fixture, invariantJobs)
	fixture.controller.mu.Lock()
	before := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
	verifiedAt := stream.verifiedAt
	fixture.controller.mu.Unlock()
	client.mode.Store(2)
	fixture.now.Add(int64(time.Minute))
	if err := fixture.controller.verifyStream(context.Background(), key); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow page error = %v, want deadline exceeded", err)
	}
	fixture.controller.mu.Lock()
	after := fixture.controller.proofs[refKey(objectReference(job, job.GroupVersionKind()))]
	afterVerified := stream.verifiedAt
	fixture.controller.mu.Unlock()
	if after != before || !afterVerified.Equal(verifiedAt) {
		t.Fatal("timed-out page renewed verification")
	}
}

func (c *invariantDeadlineClient) IsWatchListSemanticsUnSupported() bool { return true }

func invariantDebug(t *testing.T, fixture *connectedFixture) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		view := fixture.store.Capture()
		t.Logf("publication records=%+v health=%+v", view.Records, view.Health)
		fixture.controller.mu.Lock()
		defer fixture.controller.mu.Unlock()
		c := fixture.controller
		t.Logf("ready namespace=%v definition=%v namespaceError=%v definitionError=%v objects=%+v proofs=%+v membership=%+v", c.namespaceReady, c.definitionsReady, c.namespaceError, c.definitionsError, c.reader.objects, c.proofs, c.membership)
		for key, stream := range c.reader.streams {
			t.Logf("stream %v ready=%v full=%v error=%v verified=%v", key, stream.ready, stream.full, stream.err, stream.verifiedAt)
		}
	})
}

func TestRuntimeInvariantInitialCompleteMembership(t *testing.T) {
	job := runtimeJob("work", "root")
	job.SetAnnotations(map[string]string{"submitter": "alice"})
	fixture := startConnected(t, []*unstructured.Unstructured{job, runtimePod("worker", "pod", job)}, nil,
		`"submitter" in annotations ? annotations["submitter"] : ""`)
	invariantDebug(t, fixture)
	awaitRuntime(t, func() bool {
		view, record := invariantPublication(fixture, "root")
		if record == nil {
			return false
		}
		membership, _ := view.Valid(record.Membership)
		return membership && record.SubmittedBy == "alice"
	})
}
