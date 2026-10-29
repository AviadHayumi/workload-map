//go:build e2e

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type sample struct {
	family string
	labels map[string]string
	value  float64
}

var phases = []string{"Initializing", "Running", "Completed", "Failed", "Degraded", "Suspended", "Suspending", "Resuming", "Undefined"}

func createFixture(ctx context.Context, path, namespace string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	source := filepath.Join(repository, "test/e2e/flows/testdata", path)
	if local, ok := strings.CutPrefix(path, "local:"); ok {
		source = filepath.Join("fixtures", local)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	data, err = yaml.ToJSON(data)
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	object := &unstructured.Unstructured{}
	if err = json.Unmarshal(data, object); err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	if _, exists := object.Object["status"]; exists {
		return nil, schema.GroupVersionResource{}, fmt.Errorf("fixture %s must not seed controller status", path)
	}
	object.SetNamespace(namespace)
	labels := object.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[teamLabel] = "initial"
	object.SetLabels(labels)
	mapping, err := mapper.RESTMapping(object.GroupVersionKind().GroupKind(), object.GroupVersionKind().Version)
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	created, err := client.Resource(mapping.Resource).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{})
	return created, mapping.Resource, err
}

// ownerRoot reads controller owner references directly. It does not use Karta
// definitions, exporter Discovery or metrics to choose the expected root.
func ownerRoot(ctx context.Context, object *unstructured.Unstructured, cache map[types.UID]*unstructured.Unstructured) (*unstructured.Unstructured, error) {
	visited := make(map[types.UID]bool)
	for depth := 0; depth < 32; depth++ {
		if visited[object.GetUID()] {
			return nil, fmt.Errorf("owner cycle at %s", object.GetUID())
		}
		visited[object.GetUID()] = true
		var owner *metav1.OwnerReference
		for _, reference := range object.GetOwnerReferences() {
			if reference.Controller != nil && *reference.Controller {
				if owner != nil {
					return nil, fmt.Errorf("multiple controller owners")
				}
				copy := reference
				owner = &copy
			}
		}
		if owner == nil {
			return object, nil
		}
		next := cache[owner.UID]
		if next == nil {
			version, err := schema.ParseGroupVersion(owner.APIVersion)
			if err != nil {
				return nil, err
			}
			mapping, err := mapper.RESTMapping(schema.GroupKind{Group: version.Group, Kind: owner.Kind}, version.Version)
			if err != nil {
				return nil, err
			}
			next, err = client.Resource(mapping.Resource).Namespace(object.GetNamespace()).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			if next.GetUID() != owner.UID {
				return nil, fmt.Errorf("owner UID mismatch for %s", owner.Name)
			}
			cache[owner.UID] = next
		}
		object = next
	}
	return nil, fmt.Errorf("owner chain exceeded fixture depth bound")
}

func ownedPods(ctx context.Context, root *unstructured.Unstructured) ([]unstructured.Unstructured, error) {
	list, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace(root.GetNamespace()).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	cache := map[types.UID]*unstructured.Unstructured{root.GetUID(): root}
	var owned []unstructured.Unstructured
	for _, pod := range list.Items {
		if pod.GetDeletionTimestamp() != nil {
			continue
		}
		parent, err := ownerRoot(ctx, &pod, cache)
		if err != nil {
			return nil, err
		}
		if parent.GetUID() == root.GetUID() {
			owned = append(owned, pod)
		}
	}
	return owned, nil
}

func scrape(ctx context.Context) ([]sample, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Accept", "text/plain; version=0.0.4")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, nil, fmt.Errorf("metrics HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, nil, err
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return nil, raw, err
	}
	var rows []sample
	for name, family := range families {
		if !strings.HasPrefix(name, "karta_") {
			continue
		}
		if !slices.Contains([]string{"karta_pod_workload_info", "karta_workload_status", "karta_workload_created_timestamp_seconds", "karta_exporter_observation_valid"}, name) {
			return nil, raw, fmt.Errorf("unexpected metric family %s", name)
		}
		for _, metric := range family.Metric {
			if metric.Gauge == nil {
				return nil, raw, fmt.Errorf("%s is not a gauge", name)
			}
			labels := make(map[string]string)
			for _, label := range metric.Label {
				if _, duplicate := labels[label.GetName()]; duplicate {
					return nil, raw, fmt.Errorf("duplicate label %s", label.GetName())
				}
				labels[label.GetName()] = label.GetValue()
			}
			rows = append(rows, sample{family: name, labels: labels, value: metric.Gauge.GetValue()})
		}
	}
	return rows, raw, nil
}

func verifyScenario(ctx context.Context, test scenario, root *unstructured.Unstructured, team, stage string) error {
	pods, err := ownedPods(ctx, root)
	if err != nil {
		return err
	}
	expectedPods := 0
	for _, count := range test.groups {
		expectedPods += count
	}
	if len(pods) != expectedPods {
		return fmt.Errorf("owned Pods=%d, want %d", len(pods), expectedPods)
	}
	owned := make(map[string]unstructured.Unstructured)
	for _, pod := range pods {
		if !condition("Ready")(&pod) {
			phase, _, _ := unstructured.NestedString(pod.Object, "status", "phase")
			if phase != "Succeeded" && phase != "Failed" {
				return fmt.Errorf("Pod %s is neither ready nor terminal", pod.GetName())
			}
		}
		owned[string(pod.GetUID())] = pod
	}
	rows, raw, err := scrape(ctx)
	if err != nil {
		return err
	}
	seenPods := make(map[string]bool)
	seenPhases := make(map[string]float64)
	groups := make(map[coordinate]int)
	created := 0
	health := make(map[string]bool)
	for _, row := range rows {
		label := row.labels
		if label["namespace"] != root.GetNamespace() {
			continue
		}
		if row.family == "karta_exporter_observation_valid" && label["scope"] == "resource_type" {
			if row.value != 1 || label["reason"] != "none" {
				return fmt.Errorf("invalid resource collection: %v", label)
			}
			health[label["resource_group"]+"/"+label["resource_version"]+"/"+label["resource_kind"]] = true
			continue
		}
		if _, ok := owned[label["pod_uid"]]; ok && label["workload_uid"] != string(root.GetUID()) {
			return fmt.Errorf("Pod %s attributed to child or wrong root %s", label["pod_uid"], label["workload_uid"])
		}
		if label["workload_uid"] != string(root.GetUID()) {
			continue
		}
		if label["workload_name"] != root.GetName() {
			return fmt.Errorf("wrong root name: %v", label)
		}
		if row.family == "karta_exporter_observation_valid" {
			return fmt.Errorf("workload collection failed: %v", label)
		}
		if row.family == "karta_workload_created_timestamp_seconds" {
			created++
			if len(label) != 3 || row.value != float64(root.GetCreationTimestamp().Unix()) {
				return fmt.Errorf("wrong root creation timestamp")
			}
			continue
		}
		gvk := root.GroupVersionKind()
		for key, want := range map[string]string{"workload_group": gvk.Group, "workload_version": gvk.Version, "workload_kind": gvk.Kind, "owner_resolution": "resolved", "observation_valid": "true", "team": team} {
			if label[key] != want {
				return fmt.Errorf("%s %s=%q, want %q", row.family, key, label[key], want)
			}
		}
		switch row.family {
		case "karta_workload_status":
			if len(label) != 12 || label["karta"] != test.definition || label["karta_source"] != "catalog" {
				return fmt.Errorf("wrong status label contract: %v", label)
			}
			phase := label["phase"]
			if !slices.Contains(phases, phase) {
				return fmt.Errorf("unknown phase %q", phase)
			}
			if _, seen := seenPhases[phase]; seen {
				return fmt.Errorf("duplicate phase %s", phase)
			}
			seenPhases[phase] = row.value
			want := 0.0
			if slices.Contains(test.phases, phase) {
				want = 1
			}
			if row.value != want {
				return fmt.Errorf("phase %s=%v, want %v", phase, row.value, want)
			}
		case "karta_pod_workload_info":
			pod, ok := owned[label["pod_uid"]]
			if !ok || seenPods[label["pod_uid"]] || row.value != 1 || len(label) != 14 || label["pod_name"] != pod.GetName() {
				return fmt.Errorf("wrong or duplicate Pod row: %v", label)
			}
			seenPods[label["pod_uid"]] = true
			got := coordinate{label["component"], label["component_instance"], label["replica_group_id"]}
			if !validPodCoordinates(test.name, pod, got) {
				return fmt.Errorf("incorrect Pod component coordinates %s: %+v", pod.GetName(), got)
			}
			groups[got]++
		}
	}
	if created != 1 || len(seenPhases) != 9 || len(seenPods) != len(owned) || !reflect.DeepEqual(groups, test.groups) {
		return fmt.Errorf("incomplete observations: creation=%d phases=%d Pods=%d groups=%v expected=%v", created, len(seenPhases), len(seenPods), groups, test.groups)
	}
	gvk := root.GroupVersionKind()
	if !health[gvk.Group+"/"+gvk.Version+"/"+gvk.Kind] || !health["/v1/Pod"] {
		return fmt.Errorf("missing root or Pod collection health")
	}
	object, _ := json.MarshalIndent(root.Object, "", "  ")
	if err = os.WriteFile(filepath.Join(outputDir, test.name+"-"+stage+"-root.json"), object, 0600); err != nil {
		return err
	}
	podData, _ := json.MarshalIndent(pods, "", "  ")
	if err = os.WriteFile(filepath.Join(outputDir, test.name+"-"+stage+"-pods.json"), podData, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outputDir, test.name+"-"+stage+".prom"), raw, 0600)
}

func validPodCoordinates(name string, pod unstructured.Unstructured, got coordinate) bool {
	labels := pod.GetLabels()
	switch name {
	case "lws":
		component := "worker"
		if labels["leaderworkerset.sigs.k8s.io/worker-index"] == "0" {
			component = "leader"
		}
		return got == coordinate{component, "", labels["leaderworkerset.sigs.k8s.io/group-index"]}
	case "jobset":
		return got == coordinate{"replicatedjob", labels["jobset.sigs.k8s.io/replicatedjob-name"], labels["jobset.sigs.k8s.io/job-index"]}
	case "pytorch":
		return got == coordinate{labels["training.kubeflow.org/replica-type"], "", ""}
	case "mpi":
		return got == coordinate{labels["training.kubeflow.org/job-role"], "", ""}
	case "raycluster", "rayjob", "rayservice":
		component := labels["ray.io/node-type"]
		if component == "" && name == "rayjob" {
			component = "submitter"
		}
		instance := ""
		if component == "worker" {
			instance = labels["ray.io/group"]
		}
		return got == coordinate{component, instance, ""}
	case "dynamo":
		return got == coordinate{"service", labels["nvidia.com/dynamo-component"], ""}
	case "dynamo-v1beta1":
		return got == coordinate{"component", labels["nvidia.com/dynamo-component"], ""}
	case "grove":
		return got == coordinate{"clique", "worker", labels["grove.io/podcliqueset-replica-index"]}
	default:
		return true
	}
}
