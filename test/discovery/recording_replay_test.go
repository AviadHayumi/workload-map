// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/pkg/tree"
)

type recordedFlow struct {
	SchemaVersion int    `json:"schemaVersion"`
	KartaFile     string `json:"kartaFile"`
	Result        struct {
		Succeeded bool `json:"succeeded"`
	} `json:"result"`
	Events []recordedEvent `json:"events"`
}

type recordedEvent struct {
	Kind   string         `json:"kind"`
	State  string         `json:"state"`
	Phases []string       `json:"phases"`
	Object map[string]any `json:"object"`
}

// These recordings contain root objects, not descendant Pod snapshots. Each
// replay publishes explicit empty Pod coverage; grouping is exercised separately.
func TestRecordedWorkloadPhases(t *testing.T) {
	paths, corpus := recordingPaths(t)
	recordings, frames, currentFrames, treeErrors, excluded := 0, 0, 0, 0, 0
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), ".yaml"), func(t *testing.T) {
			flow, definition := readRecordedFlow(t, path, corpus)
			if _, err := catalog.Get(catalog.RootKey(definition)); err != nil {
				excluded++
				t.Logf("outside this branch's catalog: %s", definition.Name)
				return
			}
			recordings++
			for _, cache := range []bool{false, true} {
				t.Run(fmt.Sprintf("cache=%v", cache), func(t *testing.T) {
					index := sdk.NewIndex()
					options := sdk.Options{Definitions: []*v1alpha1.Karta{definition}}
					if cache {
						options.Cache = &sdk.CacheOptions{MaxWorkloads: 2, MaxPods: 16}
					}
					discoverer, err := karta.NewDiscoverer(index, options)
					if err != nil {
						t.Fatal(err)
					}
					current, err := karta.NewDiscoverer(index, sdk.Options{Cache: options.Cache})
					if err != nil {
						t.Fatal(err)
					}
					var generation uint64
					var previous *sdk.Workload
					var previousObject *unstructured.Unstructured
					var previousTree *tree.WorkloadTree
					for eventIndex, event := range flow.Events {
						if event.Kind != "STATE" {
							continue
						}
						object := &unstructured.Unstructured{Object: event.Object}
						if generation == 0 {
							generation, err = index.Replace(t.Context(), recordingDataset(object))
						} else {
							generation, err = index.Apply(t.Context(), generation, sdk.Changes{Upserts: []*unstructured.Unstructured{object}})
						}
						if err != nil {
							t.Fatalf("frame %d: %v", eventIndex, err)
						}
						observed, err := discoverer.Workload(t.Context(), recordingRef(object))
						if err != nil {
							t.Fatal(err)
						}
						got, treeErr := observed.DesiredTree(t.Context())
						assertRecordedTreeParity(t, definition, object, got, treeErr)
						if treeErr != nil {
							treeErrors++
							t.Logf("frame %d: existing tree limitation: %v", eventIndex, treeErr)
						}
						root, err := resource.NewComponentFactoryFromObject(definition, observed.Object()).GetRootComponent()
						if err != nil {
							t.Fatal(err)
						}
						status, err := root.GetStatus(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						actual := []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus}
						if status != nil && len(status.MatchedStatuses) > 0 {
							actual = status.MatchedStatuses
						}
						// Match the upstream recorder replay contract: its strongest state must
						// be present. Overlapping phases are diagnostic, not an exact oracle.
						if !slices.Contains(actual, v1alpha1.ResourceStatus(event.State)) {
							t.Errorf("frame %d: recorded state %s, got %v", eventIndex, event.State, actual)
						}
						if previous != nil && previousTree != nil {
							oldTree, treeErr := previous.DesiredTree(t.Context())
							if treeErr != nil {
								t.Fatal(treeErr)
							}
							if !reflect.DeepEqual(previousObject, previous.Object()) || !reflect.DeepEqual(previousTree, oldTree) {
								t.Fatal("later index publication changed an earlier observation")
							}
						}
						previous, previousObject, previousTree = observed, observed.Object(), got
						// Repeated acquisition must preserve the full result and recorded phases.
						again, err := discoverer.Workload(t.Context(), recordingRef(object))
						if err != nil {
							t.Fatal(err)
						}
						repeated, err := again.DesiredTree(t.Context())
						if (err == nil) != (treeErr == nil) {
							t.Fatal("repeated tree error changed")
						}
						if !reflect.DeepEqual(got, repeated) || !reflect.DeepEqual(observed.Object(), again.Object()) {
							t.Fatal("repeated acquisition changed the recorded observation")
						}
						// Current catalog expectations may differ from the historical catalog.
						// Compare its complete result to the existing offline API without
						// substituting a new expected phase for the recorder's original one.
						now, err := current.Workload(t.Context(), recordingRef(object))
						if err != nil {
							t.Fatal(err)
						}
						nowTree, err := now.DesiredTree(t.Context())
						assertRecordedTreeParity(t, now.Definition(), object, nowTree, err)
						currentFrames++
						frames++
					}
				})
			}
		})
	}
	if recordings == 0 || frames == 0 {
		t.Fatal("no supported recordings were replayed")
	}
	t.Logf("replayed %d supported recordings; %d excluded recordings; %d recorded state/frame checks; %d current-catalog tree comparisons; %d historical tree errors preserved (cache disabled and enabled)", recordings, excluded, frames, currentFrames, treeErrors)
}

func recordingPaths(t *testing.T) ([]string, string) {
	t.Helper()
	corpus := os.Getenv("KARTA_RECORDING_CORPUS")
	pattern := "testdata/recordings/*.yaml"
	if corpus != "" {
		pattern = filepath.Join(corpus, "test/e2e/recorded_data/*/*/*/*.yaml")
	}
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	paths = slices.DeleteFunc(paths, func(path string) bool { return filepath.Base(path) == "definition.yaml" })
	if len(paths) == 0 {
		t.Fatalf("no recording fixtures under %s", pattern)
	}
	return paths, corpus
}

func readRecordedFlow(t *testing.T, path, corpus string) (recordedFlow, *v1alpha1.Karta) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var flow recordedFlow
	if err := yaml.Unmarshal(raw, &flow); err != nil {
		t.Fatal(err)
	}
	for i := range flow.Events {
		if flow.Events[i].Kind != "STATE" {
			continue
		}
		object := &unstructured.Unstructured{Object: flow.Events[i].Object}
		encoded, err := object.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if err := object.UnmarshalJSON(encoded); err != nil {
			t.Fatal(err)
		}
		flow.Events[i].Object = object.Object
	}
	if flow.SchemaVersion != 1 || !flow.Result.Succeeded {
		t.Fatalf("unsupported or unsuccessful recording %s", path)
	}
	definitionPath := "testdata/recordings/definition.yaml"
	if corpus != "" {
		if filepath.Clean(flow.KartaFile) != flow.KartaFile || !strings.HasPrefix(flow.KartaFile, "docs/catalog/") || strings.Contains(flow.KartaFile, "..") {
			t.Fatal("invalid recorded definition path")
		}
		definitionPath = filepath.Join(corpus, flow.KartaFile)
	}
	raw, err = os.ReadFile(definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	var definition v1alpha1.Karta
	if err := yaml.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	return flow, &definition
}

func recordingRef(object *unstructured.Unstructured) sdk.ObjectRef {
	return sdk.ObjectRef{GVK: object.GroupVersionKind(), Namespace: object.GetNamespace(), Name: object.GetName(), UID: object.GetUID()}
}

func recordingDataset(object *unstructured.Unstructured) sdk.Dataset {
	podGVK := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	mapping := func(gvk schema.GroupVersionKind) *meta.RESTMapping {
		return &meta.RESTMapping{GroupVersionKind: gvk, Resource: gvk.GroupVersion().WithResource(strings.ToLower(gvk.Kind) + "s"), Scope: meta.RESTScopeNamespace}
	}
	result := sdk.Dataset{Namespace: object.GetNamespace(), Mappings: map[schema.GroupVersionKind]*meta.RESTMapping{podGVK: mapping(podGVK), object.GroupVersionKind(): mapping(object.GroupVersionKind())}}
	if object.GroupVersionKind() == podGVK {
		var pod corev1.Pod
		raw, err := object.MarshalJSON()
		if err != nil {
			panic(err)
		}
		if err := yaml.Unmarshal(raw, &pod); err != nil {
			panic(err)
		}
		result.Pods = []*corev1.Pod{&pod}
	} else {
		result.Objects = []*unstructured.Unstructured{object}
	}
	return result
}

func assertRecordedTreeParity(t *testing.T, definition *v1alpha1.Karta, object *unstructured.Unstructured, got *tree.WorkloadTree, gotErr error) {
	t.Helper()
	legacy, legacyErr := tree.Build(t.Context(), resource.NewComponentFactoryFromObject(definition, object))
	offline, err := karta.New(definition, object)
	if err != nil {
		t.Fatal(err)
	}
	throughInterface, interfaceErr := offline.Tree(t.Context())
	if (legacyErr == nil) != (gotErr == nil) || (interfaceErr == nil) != (gotErr == nil) {
		t.Fatalf("tree error parity: SDK=%v legacy=%v interface=%v", gotErr, legacyErr, interfaceErr)
	}
	if gotErr != nil {
		if gotErr.Error() != legacyErr.Error() || gotErr.Error() != interfaceErr.Error() {
			t.Fatal("tree errors differ")
		}
		// Only this recorded Grove limitation is expected. Shared extraction
		// failures must not turn previously successful frames into passing tests.
		const missingScalingGroups = "build instance nodes for component podcliqueset: get instance ids for component scalinggroup: failed to extract instance ids for component scalinggroup: JQ execution error for expression '.spec.template.podCliqueScalingGroups[].name': cannot iterate over: null"
		groveGVK := schema.GroupVersionKind{Group: "grove.io", Version: "v1alpha1", Kind: "PodCliqueSet"}
		groups, found, err := unstructured.NestedFieldNoCopy(object.Object, "spec", "template", "podCliqueScalingGroups")
		if object.GroupVersionKind() != groveGVK || catalog.RootKey(definition) != groveGVK ||
			gotErr.Error() != missingScalingGroups || err != nil || (found && groups != nil) {
			t.Fatalf("unexpected desired-tree failure: %v", gotErr)
		}
		return
	}
	if !reflect.DeepEqual(legacy, got) || !reflect.DeepEqual(throughInterface, got) {
		t.Fatal("discovered tree differs from the existing extraction APIs")
	}
}
