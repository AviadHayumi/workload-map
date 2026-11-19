// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package replay

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/itchyny/gojq"
	"sigs.k8s.io/yaml"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

type parityRead struct {
	name       string
	read       *api.ReadAccessor
	write      *api.WriteAccessor
	legacyPath []string
	instanced  bool
}

func originalReadFields(def api.ComponentDefinition) []parityRead {
	var fields []parityRead
	add := func(name string, via *api.ValueAccessor, path ...string) {
		if via != nil && via.Read != nil {
			fields = append(fields, parityRead{name, via.Read, via.Write, path, def.InstanceIds != nil})
		}
	}
	if s := def.SpecDefinition; s != nil {
		add("podTemplateSpec", s.PodTemplateSpec, "specDefinition", "podTemplateSpecPath")
		add("podSpec", s.PodSpec, "specDefinition", "podSpecPath")
		add("metadata", s.Metadata, "specDefinition", "metadataPath")
		if f := s.FragmentedPodSpecDefinition; f != nil {
			for _, entry := range []struct {
				name string
				via  *api.ValueAccessor
			}{{"schedulerName", f.SchedulerName}, {"labels", f.Labels}, {"annotations", f.Annotations}, {"resources", f.Resources}, {"resourceClaims", f.ResourceClaims}, {"podAffinity", f.PodAffinity}, {"nodeAffinity", f.NodeAffinity}, {"containers", f.Containers}, {"container", f.Container}, {"priorityClassName", f.PriorityClassName}, {"image", f.Image}} {
				add(entry.name, entry.via, "specDefinition", "fragmentedPodSpecDefinition", entry.name+"Path")
			}
		}
	}
	if s := def.ScaleDefinition; s != nil {
		add("replicas", s.Replicas, "scaleDefinition", "replicasPath")
		add("minReplicas", s.MinReplicas, "scaleDefinition", "minReplicasPath")
		add("maxReplicas", s.MaxReplicas, "scaleDefinition", "maxReplicasPath")
	}
	if def.InstanceIds != nil && def.InstanceIds.Read != nil {
		fields = append(fields, parityRead{name: "instanceIds", read: def.InstanceIds.Read, legacyPath: []string{"instanceIdPath"}, instanced: true})
	}
	if s := def.StatusDefinition; s != nil {
		if s.PhaseDefinition != nil {
			fields = append(fields, parityRead{name: "phase", read: &s.PhaseDefinition.Read, legacyPath: []string{"statusDefinition", "phaseDefinition", "path"}})
		}
		if s.ConditionsDefinition != nil {
			fields = append(fields, parityRead{name: "conditions", read: &s.ConditionsDefinition.Read, legacyPath: []string{"statusDefinition", "conditionsDefinition", "path"}})
		}
	}
	return fields
}

func legacyComponentMap(t *testing.T, name string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/jq-catalog", name+".yaml"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	structure := document["spec"].(map[string]any)["structureDefinition"].(map[string]any)
	components := map[string]map[string]any{}
	root := structure["rootComponent"].(map[string]any)
	components[root["name"].(string)] = root
	children, _ := structure["childComponents"].([]any)
	for _, child := range children {
		value := child.(map[string]any)
		components[value["name"].(string)] = value
	}
	return components
}

func legacyExpression(object map[string]any, path []string) string {
	current := any(object)
	for _, key := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapping[key]
	}
	value, _ := current.(string)
	return value
}

func jqResults(code *gojq.Code, object any) ([]any, error) {
	values := []any{}
	iter := code.RunWithContext(context.Background(), object)
	for {
		value, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := value.(error); ok {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// This suite evaluates the original upstream jq source and the migrated CEL
// source on the same inputs. It also compares assignable jq destinations with
// each migrated JSON Pointer. Computed jq results have no assignable path.
func TestOriginalJQReadAndDestinationParity(t *testing.T) {
	cache := map[string]*gojq.Code{}
	compile := func(source string) (*gojq.Code, error) {
		if code := cache[source]; code != nil {
			return code, nil
		}
		query, err := gojq.Parse(source)
		if err != nil {
			return nil, err
		}
		code, err := gojq.Compile(query)
		if err == nil {
			cache[source] = code
		}
		return code, err
	}
	var reads, paths, correctedEmptyGroups int
	computed := map[string]bool{}
	added := map[string]bool{}
	for _, sample := range replayCases(t) {
		t.Run(sample.name, func(t *testing.T) {
			definition := sample.definition
			legacy := legacyComponentMap(t, definition.Name)
			if legacy == nil {
				if definition.Name != "trainer-kubeflow-org-trainjob-v1alpha1" {
					t.Fatalf("missing jq baseline for %s", definition.Name)
				}
				return
			}
			variables := make([]expression.NamedExpression, len(definition.Spec.Variables))
			for i, v := range definition.Spec.Variables {
				variables[i] = expression.NamedExpression{Name: v.Name, Expression: v.Expression}
			}
			reader := &recordedReader{objects: sample.references}
			provider := func(ctx context.Context) (map[string]any, error) {
				resolved, err := references.ResolveDefinitions(ctx, reader, definition.Spec.StructureDefinition.References, sample.object)
				if err != nil {
					return nil, err
				}
				return resolved.Bindings()
			}
			runner, err := cel.NewRunnerWithVariables(sample.object, variables, cel.WithReferenceProvider(provider))
			if err != nil {
				t.Fatal(err)
			}
			factory := resource.NewComponentFactoryFromObject(definition, sample.object, resource.WithReferenceReader(reader))
			definitions := append([]api.ComponentDefinition{definition.Spec.StructureDefinition.RootComponent}, definition.Spec.StructureDefinition.ChildComponents...)
			for _, def := range definitions {
				for _, field := range originalReadFields(def) {
					key := definition.Name + "/" + def.Name + "/" + field.name
					source := legacyExpression(legacy[def.Name], field.legacyPath)
					if source == "" {
						added[key] = true
						continue
					}
					code, err := compile(source)
					if err != nil {
						t.Fatal(err)
					}
					before, jqErr := jqResults(code, normalizedReplayJSON(t, sample.object.Object))
					after := []any{}
					matched := true
					if field.read.MatchExpression != "" {
						result, err := runner.EvaluateWithVariables(context.Background(), field.read.MatchExpression, nil)
						if err != nil {
							t.Errorf("%s match: %v", key, err)
							continue
						}
						if len(result) != 1 {
							t.Fatal("invalid match result")
						}
						matched, _ = result[0].(bool)
					}
					if matched {
						after, err = runner.EvaluateWithVariables(context.Background(), field.read.Expression, nil)
						if err != nil {
							t.Errorf("%s CEL read: %v", key, err)
							continue
						}
						if field.instanced && len(after) == 1 {
							if list, ok := after[0].([]any); ok {
								after = list
							}
						}
					}
					if jqErr != nil && definition.Name == "grove-io-podcliqueset-v1alpha1" && def.Name == "scalinggroup" && (field.name == "instanceIds" || field.name == "replicas" || field.name == "minReplicas" || field.name == "maxReplicas") {
						raw := sample.object.Object["spec"].(map[string]any)["template"].(map[string]any)["podCliqueScalingGroups"]
						if raw != nil || len(after) != 0 || jqErr.Error() != "cannot iterate over: null" {
							t.Errorf("unexpected Grove correction for %s: original=%v CEL=%v", key, jqErr, after)
						}
						correctedEmptyGroups++
						continue
					}
					if jqErr != nil {
						t.Errorf("%s original jq error %v; CEL returned %s", key, jqErr, jsonTextForReplay(t, after))
						continue
					}
					if jsonTextForReplay(t, before) != jsonTextForReplay(t, after) {
						t.Errorf("%s values differ: jq=%s cel=%s", key, jsonTextForReplay(t, before), jsonTextForReplay(t, after))
						continue
					}
					reads++
					if field.write == nil {
						continue
					}
					pathCode, err := compile("path(" + source + ")")
					if err != nil {
						computed[key] = true
						continue
					}
					oldPaths, pathErr := jqResults(pathCode, normalizedReplayJSON(t, sample.object.Object))
					if pathErr != nil {
						computed[key] = true
						continue
					}
					component, err := factory.GetComponent(def.Name)
					if err != nil {
						t.Fatal(err)
					}
					ids, err := component.GetInstanceIds(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					for index, id := range ids {
						target, err := factory.ResolveWriteTarget(context.Background(), &api.ValueAccessor{Write: field.write}, id, index)
						if err != nil && field.write.MatchExpression != "" && err.Error() == "write accessor does not match the workload" {
							continue
						}
						if err != nil {
							t.Errorf("%s target: %v", key, err)
							continue
						}
						if index >= len(oldPaths) {
							t.Errorf("%s lacks jq target index %d", key, index)
							continue
						}
						pointerParts := jqPointerParts(t, sample.object.Object, target.Path)
						if !reflect.DeepEqual(normalizedReplayJSON(t, oldPaths[index]), normalizedReplayJSON(t, pointerParts)) {
							t.Errorf("%s destination differs: jq=%v cel=%v", key, oldPaths[index], pointerParts)
							continue
						}
						paths++
					}
				}
			}
		})
	}
	if reads == 0 || paths == 0 {
		t.Fatal("original jq parity exercised no reads or paths")
	}
	sorted := func(values map[string]bool) []string {
		out := make([]string, 0, len(values))
		for value := range values {
			out = append(out, value)
		}
		sort.Strings(out)
		return out
	}
	t.Logf("compared %d original jq reads and %d original assignable destinations; verified %d absent-Grove-group corrections", reads, paths, correctedEmptyGroups)
	t.Logf("computed jq fields without assignable destinations: %s", strings.Join(sorted(computed), ", "))
	t.Logf("new CEL fields without jq counterparts: %s", strings.Join(sorted(added), ", "))
}
