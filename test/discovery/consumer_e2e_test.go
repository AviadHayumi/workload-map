// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	example "github.com/dsx-ai-factory/workload-map/examples/workloadinterface"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

type consumerE2EMode struct {
	name       string
	discoverer karta.Discoverer
}

// The controller writes every status transition. Completion is gated by an exec
// in this test's Pod so a slow test cannot miss the Running observation.
func TestLiveConsumerJobLifecycle(t *testing.T) {
	if os.Getenv("KARTA_DISCOVERY_MUTATION_TESTS") != "true" {
		t.Skip("set KARTA_DISCOVERY_MUTATION_TESTS=true for isolated consumer E2E tests")
	}
	ctx, _, client, _, reader := liveClient(t)
	namespace := consumerE2ENamespace(t, ctx, client)
	jobs := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace(namespace)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": "finite-consumer"},
		"spec": map[string]any{"parallelism": int64(1), "completions": int64(1), "backoffLimit": int64(0), "template": map[string]any{
			"spec": map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{
				"name": "work", "image": "busybox:1.37", "command": []any{"sh", "-c", "until test -f /tmp/complete; do sleep 1; done"},
			}}},
		}},
	}}
	definition, err := catalog.Get(object.GroupVersionKind())
	if err != nil {
		t.Fatal(err)
	}
	editor, err := karta.New(definition, object)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.Suspend(ctx); err != nil {
		t.Fatal(err)
	}
	suspended, err := editor.Object()
	if err != nil {
		t.Fatal(err)
	}
	root, err := jobs.Create(ctx, suspended.(*unstructured.Unstructured), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitLive(t, ctx, func() (bool, error) {
		root, err = jobs.Get(ctx, root.GetName(), metav1.GetOptions{})
		return err == nil && consumerE2ECondition(root, "Suspended", "True"), err
	})
	index := sdk.NewIndex()
	modes := consumerE2EModes(t, reader, index)
	consumerE2ERefresh(t, ctx, reader, index, namespace, root)
	before := consumerE2EObserve(t, ctx, modes, liveRef(root), nil, "Suspended")

	// Kubernetes allows scheduling metadata edits on a suspended, never-started
	// Job. The published template must also reach its eventual real Pod.
	root = consumerE2EUpdate(t, ctx, jobs, root.GetName(), func(workload karta.Workload) error {
		return workload.UpdatePodTemplate(ctx, "job", karta.PodPatch{Labels: map[string]string{"sdk.example.com/template": "published"}})
	})
	label, _, err := unstructured.NestedString(root.Object, "spec", "template", "metadata", "labels", "sdk.example.com/template")
	if err != nil || label != "published" {
		t.Fatalf("persisted template label = %q: %v", label, err)
	}
	root = consumerE2EUpdate(t, ctx, jobs, root.GetName(), func(workload karta.Workload) error { return workload.Resume(ctx) })
	var runningPods []*corev1.Pod
	waitLive(t, ctx, func() (bool, error) {
		root, err = jobs.Get(ctx, root.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		ready, _, err := unstructured.NestedInt64(root.Object, "status", "ready")
		if err != nil || ready != 1 {
			return false, err
		}
		list, err := consumerE2EAPIPods(ctx, client, namespace)
		if err != nil || len(list.Items) != 1 {
			return false, err
		}
		pod := list.Items[0].DeepCopy()
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		runningPods = []*corev1.Pod{pod}
		return true, nil
	})
	if runningPods[0].Labels["sdk.example.com/template"] != "published" {
		t.Fatal("controller-created Pod did not inherit the SDK template mutation")
	}
	consumerE2ERefresh(t, ctx, reader, index, namespace, root)
	running := consumerE2EObserve(t, ctx, modes, liveRef(root), runningPods, "Running")
	for _, mode := range modes {
		observed := running[mode.name]
		component, err := observed.Component("job")
		if err != nil {
			t.Fatal(err)
		}
		instance, err := component.Instance("")
		if err != nil {
			t.Fatal(err)
		}
		pods, err := instance.Pods()
		if err != nil {
			t.Fatal(err)
		}
		consumerE2EPods(t, pods, runningPods)
		groups, err := example.SchedulingGroups(ctx, observed)
		if err != nil || len(groups) != 1 {
			t.Fatalf("%s Job scheduling groups: %v, %v", mode.name, groups, err)
		}
		if groups[0].Name != "job" || !slices.Equal(groups[0].Keys, []string{root.GetName()}) {
			t.Fatalf("%s Job scheduling identity = %s %v", mode.name, groups[0].Name, groups[0].Keys)
		}
		consumerE2EPods(t, groups[0].Pods, runningPods)
	}

	selected := os.Getenv("KARTA_DISCOVERY_CONTEXT")
	if selected == "" {
		selected = "kind-karta-sdk-discovery"
	}
	command := exec.CommandContext(ctx, "kubectl", "--kubeconfig", os.Getenv("KARTA_DISCOVERY_KUBECONFIG"),
		"--context", selected, "--namespace", namespace, "exec", runningPods[0].Name, "--container", "work", "--", "touch", "/tmp/complete")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release completion gate: %v: %s", err, output)
	}
	var completedPods []*corev1.Pod
	waitLive(t, ctx, func() (bool, error) {
		root, err = jobs.Get(ctx, root.GetName(), metav1.GetOptions{})
		if err != nil || !consumerE2ECondition(root, "Complete", "True") {
			return false, err
		}
		list, err := consumerE2EAPIPods(ctx, client, namespace)
		if err != nil || len(list.Items) != 1 {
			return false, err
		}
		if list.Items[0].Status.Phase != corev1.PodSucceeded {
			return false, nil
		}
		completedPods = []*corev1.Pod{list.Items[0].DeepCopy()}
		return true, nil
	})
	consumerE2ERefresh(t, ctx, reader, index, namespace, root)
	consumerE2EObserve(t, ctx, modes, liveRef(root), completedPods, "Completed")
	for _, mode := range modes {
		consumerE2EPhase(t, ctx, before[mode.name], "Suspended")
		consumerE2EPods(t, before[mode.name].Pods(), nil)
		consumerE2EPhase(t, ctx, running[mode.name], "Running")
		consumerE2EPods(t, running[mode.name].Pods(), runningPods)
		oldLabel, _, err := unstructured.NestedString(before[mode.name].Object().Object, "spec", "template", "metadata", "labels", "sdk.example.com/template")
		if err != nil || oldLabel != "" {
			t.Fatalf("%s persisted mutation changed previous observation: %q %v", mode.name, oldLabel, err)
		}
	}
	t.Log("real Job: persisted Suspend, legal template edit, Resume, Running and Completed; full Pods, gangs and prior observations checked in direct/index/cache modes")
}

func TestLiveConsumerJobSetScopes(t *testing.T) {
	if os.Getenv("KARTA_DISCOVERY_MUTATION_TESTS") != "true" {
		t.Skip("set KARTA_DISCOVERY_MUTATION_TESTS=true for isolated consumer E2E tests")
	}
	ctx, _, client, _, reader := liveClient(t)
	namespace := consumerE2ENamespace(t, ctx, client)
	roots := client.Resource(schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}).Namespace(namespace)
	replicatedJobs := []any{}
	for _, instance := range []struct {
		name     string
		replicas int64
	}{{"chief", 1}, {"workers", 2}} {
		replicatedJobs = append(replicatedJobs, map[string]any{"name": instance.name, "replicas": instance.replicas, "template": map[string]any{"spec": map[string]any{
			"parallelism": int64(1), "completions": int64(1), "template": map[string]any{"spec": map[string]any{
				"restartPolicy": "Never", "containers": []any{map[string]any{"name": "work", "image": "busybox:1.37", "command": []any{"sh", "-c", "sleep 600"}}},
			}},
		}}})
	}
	root, err := roots.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "jobset.x-k8s.io/v1alpha2", "kind": "JobSet", "metadata": map[string]any{"name": "scoped-consumer"},
		"spec": map[string]any{"suspend": true, "replicatedJobs": replicatedJobs},
	}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := karta.NewDiscoverer(reader, sdk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := direct.Workload(ctx, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	editor, err := karta.New(captured.Definition(), captured.Object())
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.UpdatePodTemplate(ctx, "replicatedjob", karta.PodPatch{Labels: map[string]string{"sdk.example.com/selected": "true"}}, karta.WithInstances("workers")); err != nil {
		t.Fatal(err)
	}
	edited, err := editor.Object()
	if err != nil {
		t.Fatal(err)
	}
	editedJobs, _, err := unstructured.NestedSlice(edited.(*unstructured.Unstructured).Object, "spec", "replicatedJobs")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range editedJobs {
		instance := item.(map[string]any)
		label, _, err := unstructured.NestedString(instance, "template", "spec", "template", "metadata", "labels", "sdk.example.com/selected")
		if err != nil || (label == "true") != (instance["name"] == "workers") {
			t.Fatalf("named-template edit selected wrong instance %v: %q %v", instance["name"], label, err)
		}
	}
	// JobSet v0.10.1 allows scheduling metadata edits while suspended. Persist
	// the named mutation through the same SDK operation after each conflict.
	root = consumerE2EUpdate(t, ctx, roots, root.GetName(), func(workload karta.Workload) error {
		return workload.UpdatePodTemplate(ctx, "replicatedjob", karta.PodPatch{Labels: map[string]string{"sdk.example.com/selected": "true"}}, karta.WithInstances("workers"))
	})
	beforeJobs, _, err := unstructured.NestedSlice(captured.Object().Object, "spec", "replicatedJobs")
	if err != nil {
		t.Fatal(err)
	}
	afterJobs, _, err := unstructured.NestedSlice(root.Object, "spec", "replicatedJobs")
	if err != nil || !reflect.DeepEqual(editedJobs, afterJobs) {
		t.Fatalf("persisted named-template update differs from the local edit: %v", err)
	}
	if reflect.DeepEqual(beforeJobs, afterJobs) {
		t.Fatal("named-template update did not change live declarations")
	}
	root = consumerE2EUpdate(t, ctx, roots, root.GetName(), func(workload karta.Workload) error { return workload.Resume(ctx) })
	var expected []*corev1.Pod
	waitLive(t, ctx, func() (bool, error) {
		root, err = roots.Get(ctx, root.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		statuses, _, err := unstructured.NestedSlice(root.Object, "status", "replicatedJobsStatus")
		if err != nil || len(statuses) != 2 {
			return false, err
		}
		for _, item := range statuses {
			status := item.(map[string]any)
			ready, _, err := unstructured.NestedInt64(status, "ready")
			active, _, activeErr := unstructured.NestedInt64(status, "active")
			if err != nil || activeErr != nil {
				return false, fmt.Errorf("JobSet controller status: %v, %v", err, activeErr)
			}
			want := map[string]int64{"chief": 1, "workers": 2}[status["name"].(string)]
			if want == 0 {
				return false, fmt.Errorf("unexpected JobSet status instance %v", status["name"])
			}
			if ready != want || active != want {
				return false, nil
			}
		}
		list, err := consumerE2EAPIPods(ctx, client, namespace)
		if err != nil || len(list.Items) != 3 {
			return false, err
		}
		var pods []*corev1.Pod
		for i := range list.Items {
			pod := &list.Items[i]
			if pod.Status.Phase != corev1.PodRunning {
				return false, nil
			}
			pods = append(pods, pod.DeepCopy())
		}
		expected = pods
		return true, nil
	})
	// Expectations come from the submitted instance counts and upstream labels,
	// with each Pod's actual Job owner independently checked against the root UID.
	expectedScopes := map[string]map[string][]*corev1.Pod{"chief": {"0": nil}, "workers": {"0": nil, "1": nil}}
	jobs := client.Resource(schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}).Namespace(namespace)
	for _, pod := range expected {
		instance := pod.Labels["jobset.sigs.k8s.io/replicatedjob-name"]
		if (pod.Labels["sdk.example.com/selected"] == "true") != (instance == "workers") {
			t.Fatalf("named mutation did not select only workers Pods: %s", pod.Name)
		}
		replica := pod.Labels["jobset.sigs.k8s.io/job-index"]
		if _, ok := expectedScopes[instance][replica]; !ok {
			t.Fatalf("unexpected operator instance/replica: %s/%s", instance, replica)
		}
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "Job" || owner.APIVersion != "batch/v1" {
			t.Fatalf("Pod %s lacks its Job owner", pod.Name)
		}
		job, err := jobs.Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rootOwner := metav1.GetControllerOf(job)
		if job.GetUID() != owner.UID || rootOwner == nil || rootOwner.UID != root.GetUID() {
			t.Fatalf("Pod %s is not owned by this JobSet", pod.Name)
		}
		expectedScopes[instance][replica] = append(expectedScopes[instance][replica], pod)
	}
	index := sdk.NewIndex()
	modes := consumerE2EModes(t, reader, index)
	consumerE2ERefresh(t, ctx, reader, index, namespace, root)
	observations := consumerE2EObserve(t, ctx, modes, liveRef(root), expected, "Running")
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			observed := observations[mode.name]
			component, err := observed.Component("replicatedjob")
			if err != nil {
				t.Fatal(err)
			}
			groups, err := example.SchedulingGroups(ctx, observed)
			if err != nil || len(groups) != 2 {
				t.Fatalf("scheduling groups: %v, %v", groups, err)
			}
			for _, name := range []string{"chief", "workers"} {
				instance, err := component.Instance(name)
				if err != nil {
					t.Fatal(err)
				}
				var all []*corev1.Pod
				for id, want := range expectedScopes[name] {
					if len(want) != 1 {
						t.Fatalf("fixture %s/%s has %d Pods, want 1", name, id, len(want))
					}
					replica, err := instance.Replica(id)
					if err != nil {
						t.Fatal(err)
					}
					pods, err := replica.Pods()
					if err != nil {
						t.Fatal(err)
					}
					consumerE2EPods(t, pods, want)
					all = append(all, want...)
				}
				pods, err := instance.Pods()
				if err != nil {
					t.Fatal(err)
				}
				consumerE2EPods(t, pods, all)
				matched := false
				for _, group := range groups {
					if group.Name == "job" && slices.Equal(group.Keys, []string{name}) {
						consumerE2EPods(t, group.Pods, all)
						matched = true
					}
				}
				if !matched {
					t.Fatalf("missing gang for %s", name)
				}
			}
			desired, err := observed.DesiredTree(ctx)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]int32{}
			for component, instance := range desired.Instances() {
				if component.Name != "replicatedjob" || instance.InstanceKey == nil || instance.Scale == nil || instance.Scale.Replicas == nil {
					t.Fatal("desired traversal lost named replicated Job scale")
				}
				seen[*instance.InstanceKey] = *instance.Scale.Replicas
			}
			if !reflect.DeepEqual(seen, map[string]int32{"chief": 1, "workers": 2}) {
				t.Fatalf("desired traversal = %v", seen)
			}
		})
	}
	consumerE2EPods(t, captured.Pods(), nil)
	retainedJobs, _, err := unstructured.NestedSlice(captured.Object().Object, "spec", "replicatedJobs")
	if err != nil || !reflect.DeepEqual(beforeJobs, retainedJobs) {
		t.Fatalf("prior captured declarations changed: %v", err)
	}
	t.Log("real JobSet: persisted named workers-only template edit; Pod->root->component/instance/replica/full Pods and scheduling gangs checked in direct/index/cache modes")
}

func consumerE2ENamespace(t *testing.T, ctx context.Context, client dynamic.Interface) string {
	t.Helper()
	namespaces := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	namespace := fmt.Sprintf("sdk-consumer-%d", time.Now().UnixNano())
	if _, err := namespaces.Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := namespaces.Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete consumer E2E namespace: %v", err)
		}
	})
	return namespace
}

func consumerE2EModes(t *testing.T, reader sdk.Reader, index *sdk.Index) []consumerE2EMode {
	t.Helper()
	var modes []consumerE2EMode
	for _, mode := range []struct {
		name    string
		reader  sdk.Reader
		options sdk.Options
	}{
		{name: "direct", reader: reader},
		{name: "indexed", reader: index},
		{name: "cached", reader: index, options: sdk.Options{Cache: &sdk.CacheOptions{MaxWorkloads: 4, MaxPods: 16}}},
	} {
		discoverer, err := karta.NewDiscoverer(mode.reader, mode.options)
		if err != nil {
			t.Fatal(err)
		}
		modes = append(modes, consumerE2EMode{name: mode.name, discoverer: discoverer})
	}
	return modes
}

func consumerE2ERefresh(t *testing.T, ctx context.Context, reader sdk.Reader, index *sdk.Index, namespace string, root *unstructured.Unstructured) {
	t.Helper()
	dataset, err := sdk.Collect(ctx, reader, namespace, liveRef(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
}

func consumerE2EObserve(t *testing.T, ctx context.Context, modes []consumerE2EMode, root sdk.ObjectRef, expected []*corev1.Pod, phase string) map[string]*sdk.Workload {
	t.Helper()
	observations := make(map[string]*sdk.Workload)
	for _, mode := range modes {
		for range 2 {
			observed, err := mode.discoverer.Workload(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			if observed.Root() != root || observed.Generic() || len(observed.MembershipIssues()) != 0 {
				t.Fatalf("%s lost complete known workload attribution", mode.name)
			}
			consumerE2EPods(t, observed.Pods(), expected)
			// Returned values belong to the caller, including nested Pod fields.
			object := observed.Object()
			object.SetLabels(map[string]string{"sdk.example.com/local": "changed"})
			if observed.Object().GetLabels()["sdk.example.com/local"] != "" {
				t.Fatalf("%s returned a shared root object", mode.name)
			}
			returned := observed.Pods()
			if len(returned) > 0 {
				returned[0].Labels["sdk.example.com/local"] = "changed"
				returned[0].Spec.Containers[0].Image = "example.invalid/local-change:v1"
				returned[0].Status.Phase = corev1.PodUnknown
				consumerE2EPods(t, observed.Pods(), expected)
			}
			if phase != "" {
				consumerE2EPhase(t, ctx, observed, phase)
			}
			for _, pod := range expected {
				fromPod, err := mode.discoverer.WorkloadForPod(ctx, sdk.ObjectRef{GVK: schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID})
				if err != nil {
					t.Fatal(err)
				}
				if fromPod.Root() != root {
					t.Fatalf("%s Pod lookup returned another root", mode.name)
				}
				consumerE2EPods(t, fromPod.Pods(), expected)
			}
			observations[mode.name] = observed
		}
	}
	return observations
}

// Read the API independently so SDK decoding cannot alter both expected and observed Pods.
func consumerE2EAPIPods(ctx context.Context, client dynamic.Interface, namespace string) (*corev1.PodList, error) {
	objects, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := &corev1.PodList{}
	for i := range objects.Items {
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(objects.Items[i].Object, &pod); err != nil {
			return nil, fmt.Errorf("decode expected Pod %s: %w", objects.Items[i].GetName(), err)
		}
		if pod.APIVersion == "" && pod.Kind == "" {
			pod.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}
		} else if pod.APIVersion != "v1" || pod.Kind != "Pod" {
			return nil, fmt.Errorf("expected Pod %s has conflicting GVK %s", pod.Name, pod.GroupVersionKind())
		}
		result.Items = append(result.Items, pod)
	}
	return result, nil
}

func consumerE2EPods(t *testing.T, got, want []*corev1.Pod) {
	t.Helper()
	byUID := make(map[string]*corev1.Pod, len(want))
	for _, pod := range want {
		byUID[string(pod.UID)] = pod
	}
	if len(got) != len(want) {
		t.Fatalf("full Pod count = %d, want %d", len(got), len(want))
	}
	seen := make(map[string]bool, len(got))
	for _, pod := range got {
		uid := string(pod.UID)
		if seen[uid] || !reflect.DeepEqual(pod, byUID[uid]) {
			t.Fatalf("full Pod body mismatch or duplicate for %s/%s", pod.Namespace, pod.Name)
		}
		seen[uid] = true
	}
}

func consumerE2EPhase(t *testing.T, ctx context.Context, observed *sdk.Workload, phase string) {
	t.Helper()
	desired, err := observed.DesiredTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if desired.Status == nil || !slices.Contains(desired.Status.Phases, phase) {
		t.Fatalf("desired phases = %v, want %s", desired.Status, phase)
	}
	if desired.Root == nil || len(desired.Root.Instances) != 1 || desired.RootStatus == nil {
		t.Fatal("desired tree lost root instance or raw status")
	}
	conditions, _, err := unstructured.NestedSlice(observed.Object().Object, "status", "conditions")
	if err != nil || len(conditions) != len(desired.RootStatus.Conditions) {
		t.Fatalf("raw root condition count changed: %v", err)
	}
	for i, item := range conditions {
		raw := item.(map[string]any)
		condition := desired.RootStatus.Conditions[i]
		if condition.Type != raw["type"] || condition.Status == nil || *condition.Status != raw["status"] {
			t.Fatalf("raw root condition %d changed: %+v", i, condition)
		}
		if reason, ok := raw["reason"].(string); ok && (condition.Reason == nil || *condition.Reason != reason) {
			t.Fatalf("raw root reason %d changed: %+v", i, condition)
		}
		if message, ok := raw["message"].(string); ok && condition.Message != message {
			t.Fatalf("raw root message %d changed: %+v", i, condition)
		}
	}
}

func consumerE2ECondition(object *unstructured.Unstructured, kind, value string) bool {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if ok && condition["type"] == kind && condition["status"] == value {
			return true
		}
	}
	return false
}

func consumerE2EUpdate(t *testing.T, ctx context.Context, objects dynamic.ResourceInterface, name string, mutate func(karta.Workload) error) *unstructured.Unstructured {
	t.Helper()
	var updated *unstructured.Unstructured
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := objects.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		definition, err := catalog.Get(current.GroupVersionKind())
		if err != nil {
			return err
		}
		editor, err := karta.New(definition, current)
		if err != nil {
			return err
		}
		if err := mutate(editor); err != nil {
			return err
		}
		object, err := editor.Object()
		if err != nil {
			return err
		}
		updated, err = objects.Update(ctx, object.(*unstructured.Unstructured), metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}
