// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/itchyny/gojq"
	"sigs.k8s.io/yaml"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/tree"
	"github.com/dsx-ai-factory/workload-map/test/e2e/recorder"
)

type replayField struct {
	field tree.Field
	via   *api.ValueAccessor
	patch any
}

func replayFields(def api.ComponentDefinition) []replayField {
	var fields []replayField
	add := func(field tree.Field, via *api.ValueAccessor, patch any) {
		if via != nil && via.Write != nil {
			fields = append(fields, replayField{field, via, patch})
		}
	}
	if s := def.SpecDefinition; s != nil {
		add(tree.PodTemplateSpec, s.PodTemplateSpec, map[string]any{"spec": map[string]any{"schedulerName": "poc-scheduler"}})
		add(tree.PodSpec, s.PodSpec, map[string]any{"schedulerName": "poc-scheduler"})
		add(tree.Metadata, s.Metadata, map[string]any{"annotations": map[string]any{"karta-poc": "true"}})
		if f := s.FragmentedPodSpecDefinition; f != nil {
			add(tree.SchedulerName, f.SchedulerName, "poc-scheduler")
			add(tree.LabelsField, f.Labels, map[string]any{"karta-poc": "true"})
			add(tree.Annotations, f.Annotations, map[string]any{"karta-poc": "true"})
			add(tree.Resources, f.Resources, map[string]any{"requests": map[string]any{"cpu": "100m"}})
			add(tree.ResourceClaims, f.ResourceClaims, []any{map[string]any{"name": "poc-claim"}})
			add(tree.PodAffinity, f.PodAffinity, map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{}})
			add(tree.NodeAffinity, f.NodeAffinity, map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchExpressions": []any{map[string]any{"key": "poc-pool", "operator": "In", "values": []any{"cpu"}}}}}}})
			add(tree.Containers, f.Containers, []any{map[string]any{"name": "poc-container", "image": "ghcr.io/example/poc:v1"}})
			add(tree.Container, f.Container, map[string]any{"image": "ghcr.io/example/poc:v1"})
			add(tree.PriorityClassName, f.PriorityClassName, "poc-priority")
			add(tree.Image, f.Image, "ghcr.io/example/poc:v1")
		}
	}
	if s := def.ScaleDefinition; s != nil {
		add(tree.Replicas, s.Replicas, 2)
		add(tree.MinReplicas, s.MinReplicas, 1)
		add(tree.MaxReplicas, s.MaxReplicas, 10)
	}
	return fields
}

func loadReplayDefinition(t *testing.T, path string) (*recorder.Reader, *api.Karta) {
	t.Helper()
	reader, err := recorder.OpenRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, reader.Recording().KartaFile))
	if err != nil {
		t.Fatal(err)
	}
	definition := &api.Karta{}
	if err = yaml.Unmarshal(data, definition); err != nil {
		t.Fatal(err)
	}
	return reader, definition
}

func allReplayPaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(recordedGlob)
	if err != nil || len(paths) == 0 {
		t.Fatalf("recordings=%d error=%v", len(paths), err)
	}
	sort.Strings(paths)
	return paths
}

func jsonTextForReplay(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// gojq is a test oracle for RFC 7396, not an SDK evaluator. Every field uses a
// fresh real editor and a real recorded root. The complete resulting document
// must match the independent patch, including unknown operator fields.
func TestRawMutationOfEveryRecordedCatalogField(t *testing.T) {
	query, err := gojq.Parse(`def merge_patch($p): if ($p|type) == "object" then (if type == "object" then . else {} end) as $base | reduce ($p|keys[]) as $k ($base; if $p[$k] == null then del(.[$k]) else .[$k] = (.[$k] | merge_patch($p[$k])) end) else $p end; getpath($path) | merge_patch($patch)`)
	if err != nil {
		t.Fatal(err)
	}
	merge, err := gojq.Compile(query, gojq.WithVariables([]string{"$path", "$patch"}))
	if err != nil {
		t.Fatal(err)
	}
	setQuery, err := gojq.Parse(`setpath($path;$value)`)
	if err != nil {
		t.Fatal(err)
	}
	set, err := gojq.Compile(setQuery, gojq.WithVariables([]string{"$path", "$value"}))
	if err != nil {
		t.Fatal(err)
	}
	coverage := map[string]bool{}
	seenCatalogs := map[string]bool{}
	var writes, events, synthetic, guarded int
	for _, sample := range replayCases(t) {
		t.Run(sample.name, func(t *testing.T) {
			definition, object := sample.definition, sample.object
			seenCatalogs[definition.Name] = true
			if sample.recorded {
				events++
			} else {
				synthetic++
			}
			refs := &recordedReader{objects: sample.references}
			factory := resource.NewComponentFactoryFromObject(definition, object, resource.WithReferenceReader(refs))
			snapshot, err := tree.Open(context.Background(), definition, object, resource.WithReferenceReader(refs))
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Snapshot().Status == nil {
				t.Fatal("root lost status")
			}

			definitions := append([]api.ComponentDefinition{definition.Spec.StructureDefinition.RootComponent}, definition.Spec.StructureDefinition.ChildComponents...)
			for _, def := range definitions {
				component, err := factory.GetComponent(def.Name)
				if err != nil {
					t.Fatal(err)
				}
				ids, err := component.GetInstanceIds(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				for _, field := range replayFields(def) {
					for index, id := range ids {
						key := definition.Name + "/" + def.Name + "/" + string(field.field)
						// One instance of each field per recording is sufficient for repeated
						// identical frames, while every declared instance is exercised below.
						localKey := key + "/" + id
						if coverage[sample.source+"/"+localKey] {
							continue
						}
						target, err := factory.ResolveWriteTarget(context.Background(), field.via, id, index)
						if err != nil && field.via.Write.MatchExpression != "" && err.Error() == "write accessor does not match the workload" {
							before := jsonTextForReplay(t, object)
							err = snapshot.Mutate(context.Background(), tree.Write{Component: def.Name, Instance: id, Field: field.field, Value: field.patch})
							if err == nil {
								t.Fatal("guarded write succeeded")
							}
							if before != jsonTextForReplay(t, object) {
								t.Fatal("guard rejection changed input")
							}
							guarded++
							continue
						}
						if err != nil {
							t.Fatalf("%s target: %v", localKey, err)
						}
						editor, err := tree.Open(context.Background(), definition, object, resource.WithReferenceReader(refs))
						if err != nil {
							t.Fatal(err)
						}
						before := jsonTextForReplay(t, object)
						err = editor.Mutate(context.Background(), tree.Write{Component: def.Name, Instance: id, Field: field.field, Value: field.patch})
						if err != nil {
							t.Fatalf("%s at %s: %v", localKey, target.Path, err)
						}
						result, err := editor.GetResource()
						if err != nil {
							t.Fatal(err)
						}
						pathParts := jqPointerParts(t, object.Object, target.Path)
						normalized := normalizedReplayJSON(t, object.Object)
						patch := normalizedReplayJSON(t, field.patch)
						value := jqSingle(t, merge.RunWithContext(context.Background(), normalized, pathParts, patch))
						expected := jqSingle(t, set.RunWithContext(context.Background(), normalized, pathParts, value))
						if got, want := jsonTextForReplay(t, result), jsonTextForReplay(t, expected); got != want {
							t.Fatalf("%s at %s differs from jq Merge Patch oracle\ngot %s\nwant %s", localKey, target.Path, got, want)
						}
						if before != jsonTextForReplay(t, object) {
							t.Fatal("editor mutated the recording input")
						}
						coverage[key] = true
						coverage[sample.source+"/"+localKey] = true
						writes++
					}
				}
			}
		})
	}
	for _, definition := range catalog.List() {
		if !seenCatalogs[definition.Name] {
			t.Errorf("catalog %s has no recorded or synthetic input", definition.Name)
			continue
		}
		for _, def := range append([]api.ComponentDefinition{definition.Spec.StructureDefinition.RootComponent}, definition.Spec.StructureDefinition.ChildComponents...) {
			for _, field := range replayFields(def) {
				key := definition.Name + "/" + def.Name + "/" + string(field.field)
				if !coverage[key] {
					t.Errorf("writable catalog field lacks a successful replay: %s", key)
				}
			}
		}
	}
	if writes == 0 || events == 0 {
		t.Fatal("no mutation coverage")
	}
	t.Logf("checked %d writes and %d guard rejections across %d recorded states, %d synthetic inputs and %d catalogs", writes, guarded, events, synthetic, len(seenCatalogs))
}

func normalizedReplayJSON(t *testing.T, value any) any {
	t.Helper()
	var out any
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func jqSingle(t *testing.T, iter gojq.Iter) any {
	t.Helper()
	value, ok := iter.Next()
	if !ok {
		t.Fatal("jq produced no result")
	}
	if err, ok := value.(error); ok {
		t.Fatal(err)
	}
	if _, ok = iter.Next(); ok {
		t.Fatal("jq produced multiple results")
	}
	return value
}
func jqPointerParts(t *testing.T, object map[string]any, pointer string) []any {
	t.Helper()
	parts := []any{}
	if pointer == "" {
		return parts
	}
	current := any(object)
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		if array, ok := current.([]any); ok {
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(array) {
				t.Fatalf("invalid array pointer %s", pointer)
			}
			parts = append(parts, index)
			current = array[index]
		} else {
			parts = append(parts, part)
			if object, ok := current.(map[string]any); ok {
				current = object[part]
			} else {
				current = nil
			}
		}
	}
	return parts
}

func TestTrainerRecordingIncludesRuntimeAndFullLifecycle(t *testing.T) {
	paths, err := filepath.Glob("../recorded_data/trainer/*/*/*.yaml")
	if err != nil || len(paths) != 5 {
		t.Fatalf("Trainer recordings=%d err=%v", len(paths), err)
	}
	saw := map[string]bool{}
	var runtimeFrames int
	for _, path := range paths {
		reader, definition := loadReplayDefinition(t, path)
		for reader.Next() {
			saw[reader.State()] = true
			refs := reader.References()
			if len(refs) != 1 || refs[0].GetKind() != "ClusterTrainingRuntime" {
				t.Fatalf("%s lacks its actual runtime capture", path)
			}
			runtimeFrames++
			factory := resource.NewComponentFactoryFromObject(definition, reader.Object(), resource.WithReferenceReader(&recordedReader{objects: refs}))
			trainer, err := factory.GetRootComponent()
			if err != nil {
				t.Fatal(err)
			}
			instances, err := trainer.GetExtractedInstances(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(instances) == 0 {
				t.Fatal("trainer extraction returned no instance")
			}
		}
	}
	for _, state := range []string{"Initializing", "Running", "Suspended", "Completed", "Failed"} {
		if !saw[state] {
			t.Errorf("Trainer lacks %s frame", state)
		}
	}
	t.Logf("replayed %d Trainer frames with captured ClusterTrainingRuntime values", runtimeFrames)
}

type workloadCase struct {
	name, source string
	definition   *api.Karta
	object       *unstructured.Unstructured
	references   []*unstructured.Unstructured
	recorded     bool
}

func replayCases(t *testing.T) []workloadCase {
	t.Helper()
	var samples []workloadCase
	for _, path := range allReplayPaths(t) {
		reader, definition := loadReplayDefinition(t, path)
		index := 0
		for reader.Next() {
			index++
			samples = append(samples, workloadCase{name: fmt.Sprintf("%s/%03d-%s", strings.TrimPrefix(path, "../recorded_data/"), index, reader.State()), source: path, definition: definition, object: reader.Object(), references: reader.References(), recorded: true})
		}
	}
	paths, err := filepath.Glob("testdata/synthetic-catalogs/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var input struct {
			KartaFile string         `json:"kartaFile"`
			Workload  map[string]any `json:"workload"`
		}
		if err = yaml.Unmarshal(data, &input); err != nil {
			t.Fatal(err)
		}
		data, err = os.ReadFile(filepath.Join(repoRoot, input.KartaFile))
		if err != nil {
			t.Fatal(err)
		}
		definition := &api.Karta{}
		if err = yaml.Unmarshal(data, definition); err != nil {
			t.Fatal(err)
		}
		samples = append(samples, workloadCase{name: path, source: path, definition: definition, object: &unstructured.Unstructured{Object: input.Workload}})
	}
	return samples
}
