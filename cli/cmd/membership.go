// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cmd

import (
	"context"
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/dsx-ai-factory/workload-map/cli/pkg/workload"
	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/membership"
)

// observeMembership evaluates every claimant before display filters are applied.
// Each namespace is independent: an unreadable namespace cannot fold its targets.
func observeMembership(ctx context.Context, look lookup, listed []*listedKind, complete bool) (map[string]*membership.Result, []string, error) {
	inputs := map[string]membership.Input{}
	needed := map[string]bool{}
	for _, kind := range listed {
		for i := range kind.objects {
			object := &kind.objects[i]
			namespace := object.GetNamespace()
			if namespace == "" {
				continue
			}
			input := inputs[namespace]
			input.ScopeComplete = complete
			input.Workloads = append(input.Workloads, membership.WorkloadInput{Object: object, Definition: kind.def.Karta})
			inputs[namespace] = input
			needed[namespace] = needed[namespace] || hasMembershipReferences(kind.def.Karta)
		}
	}
	results := map[string]*membership.Result{}
	warnings := []string{}
	for _, namespace := range slices.Sorted(maps.Keys(inputs)) {
		input := inputs[namespace]
		if !needed[namespace] {
			continue
		}
		raw, err := workload.ListPods(ctx, look.dyn, namespace)
		if err != nil {
			if ctx.Err() != nil {
				return nil, warnings, ctx.Err()
			}
			warnings = append(warnings, fmt.Sprintf("reference membership unavailable in %s: list Pods: %v", namespace, err))
			continue
		}
		for i := range raw {
			var pod corev1.Pod
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw[i].Object, &pod); err != nil {
				return nil, warnings, fmt.Errorf("decode membership Pod: %w", err)
			}
			input.Pods = append(input.Pods, pod)
		}
		result, err := membership.Resolve(ctx, input, membership.WithReader(workload.NewReferenceReader(look.dyn, look.mapper)))
		if err != nil {
			if ctx.Err() != nil {
				return nil, warnings, ctx.Err()
			}
			warnings = append(warnings, fmt.Sprintf("reference membership unavailable in %s: %v", namespace, err))
			continue
		}
		results[namespace] = result
		warnings = append(warnings, result.Diagnostics...)
	}
	return results, warnings, nil
}

func viewIdentity(view workload.View) string {
	return view.Namespace + "/" + view.APIVersion + "/" + view.Kind + "/" + view.Name
}

func foldReferenceViews(views []workload.View, listed []*listedKind, results map[string]*membership.Result) []workload.View {
	identities := map[string]types.UID{}
	for _, kind := range listed {
		for _, obj := range kind.objects {
			identities[obj.GetNamespace()+"/"+obj.GetAPIVersion()+"/"+obj.GetKind()+"/"+obj.GetName()] = obj.GetUID()
		}
	}
	displayed := map[types.UID]bool{}
	for _, view := range views {
		if uid := identities[viewIdentity(view)]; uid != "" {
			displayed[uid] = true
		}
	}
	kept := make([]workload.View, 0, len(views))
	for _, view := range views {
		result := results[view.Namespace]
		if result != nil && result.Complete {
			if target, ok := result.Targets[identities[viewIdentity(view)]]; ok && target.Foldable && displayed[target.Parent] {
				continue
			}
		}
		kept = append(kept, view)
	}
	return kept
}

func hasMembershipReferences(definition *api.Karta) bool {
	components := append([]api.ComponentDefinition{definition.Spec.StructureDefinition.RootComponent}, definition.Spec.StructureDefinition.ChildComponents...)
	for _, component := range components {
		if component.PodSelector != nil && len(component.PodSelector.FromReferences) > 0 {
			return true
		}
	}
	return false
}

func describeComponentMembership(ctx context.Context, look lookup, object *unstructured.Unstructured) (map[string][]corev1.Pod, []string, error) {
	if object.GetNamespace() == "" || !hasMembershipReferences(look.definition.Karta) {
		return nil, nil, nil
	}
	look.namespace = object.GetNamespace()
	versions, kinds := coveredVersions(look.resolver)
	mappings, warnings, err := discoverKinds(look, versions, kinds)
	if err != nil {
		return nil, append(warnings, "reference membership unavailable: "+err.Error()), ctx.Err()
	}
	listed, listWarnings, err := listKinds(ctx, look, &getOptions{chunkSize: defaultChunkSize}, versions, kinds, mappings)
	warnings = append(warnings, listWarnings...)
	if err != nil {
		return nil, append(warnings, "reference membership unavailable: "+err.Error()), ctx.Err()
	}
	found := false
	for _, kind := range listed {
		for i := range kind.objects {
			if kind.objects[i].GetUID() == object.GetUID() {
				kind.objects[i] = *object.DeepCopy()
				found = true
			}
		}
	}
	if !found {
		return nil, append(warnings, "reference membership unavailable: workload was absent from the namespace observation"), nil
	}
	results, membershipWarnings, err := observeMembership(ctx, look, listed, len(warnings) == 0)
	warnings = append(warnings, membershipWarnings...)
	if err != nil {
		return nil, warnings, err
	}
	result := results[object.GetNamespace()]
	if result == nil || !result.Complete {
		return nil, warnings, nil
	}
	assigned := map[string][]corev1.Pod{}
	for key, pods := range result.Pods {
		if key.Workload == object.GetUID() {
			assigned[key.Component] = pods
		}
	}
	return assigned, warnings, nil
}
