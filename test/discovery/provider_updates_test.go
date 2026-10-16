// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/pkg/catalog"
	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

// The consumer owns readiness/error policy; the registry itself is ready when constructed.
type liveDefinitionProvider struct {
	registry *sdk.DefinitionRegistry
	failure  error
}

func (p *liveDefinitionProvider) Definition(ctx context.Context, gvk schema.GroupVersionKind) (*sdk.Definition, error) {
	if p.failure != nil {
		return nil, p.failure
	}
	return p.registry.Definition(ctx, gvk)
}

func TestLiveDefinitionProviderUpdates(t *testing.T) {
	ctx, _, client, mapper, reader := liveClient(t)
	path := os.Getenv("KARTA_DISCOVERY_ORACLE")
	if path == "" {
		t.Fatal("set KARTA_DISCOVERY_ORACLE to the independent fixture UID oracle")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var expected oracle
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	var root identity
	var expectedUIDs []string
	for _, workload := range expected.Workloads {
		if workload.Root.Kind == "JobSet" {
			root, expectedUIDs = workload.Root, workload.PodUIDs
		}
	}
	if root.UID == "" || len(expectedUIDs) != 3 {
		t.Fatal("oracle must contain the JobSet fixture with three Pods")
	}
	apiPods, err := consumerE2EAPIPods(ctx, client, expected.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	var expectedBodies []*corev1.Pod
	for i := range apiPods.Items {
		if slices.Contains(expectedUIDs, string(apiPods.Items[i].UID)) {
			expectedBodies = append(expectedBodies, apiPods.Items[i].DeepCopy())
		}
	}
	assertPods(t, expectedBodies, expectedUIDs)
	rootRef := root.ref()
	mapping, err := mapper.RESTMapping(rootRef.GVK.GroupKind(), rootRef.GVK.Version)
	if err != nil {
		t.Fatal(err)
	}
	apiRoot, err := client.Resource(mapping.Resource).Namespace(root.Namespace).Get(ctx, root.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := catalog.Get(rootRef.GVK)
	if err != nil {
		t.Fatal(err)
	}
	changed := definition.DeepCopy()
	changed.Spec.StructureDefinition.ChildComponents[0].Name = "jobtemplate"
	for i := range changed.Spec.Instructions.GangScheduling.PodGroups {
		for j := range changed.Spec.Instructions.GangScheduling.PodGroups[i].Members {
			changed.Spec.Instructions.GangScheduling.PodGroups[i].Members[j].ComponentName = "jobtemplate"
		}
	}
	dataset, err := sdk.Collect(ctx, reader, expected.Namespace, rootRef)
	if err != nil {
		t.Fatal(err)
	}
	index := sdk.NewIndex()
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []struct {
		name    string
		reader  sdk.Reader
		options sdk.Options
	}{
		{name: "direct", reader: reader},
		{name: "indexed", reader: index},
		{name: "cached", reader: index, options: sdk.Options{Cache: &sdk.CacheOptions{MaxWorkloads: 2, MaxPods: 6}}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			registry, err := sdk.NewDefinitionRegistry(definition)
			if err != nil {
				t.Fatal(err)
			}
			provider := &liveDefinitionProvider{registry: registry}
			mode.options.DefinitionProvider = provider
			discoverer, err := karta.NewDiscoverer(mode.reader, mode.options)
			if err != nil {
				t.Fatal(err)
			}
			observe := func(component string) *sdk.Workload {
				t.Helper()
				var result *sdk.Workload
				for query := -1; query < len(expectedBodies); query++ {
					var observed *sdk.Workload
					var err error
					if query < 0 {
						observed, err = discoverer.Workload(ctx, rootRef)
					} else {
						pod := expectedBodies[query]
						observed, err = discoverer.WorkloadForPod(ctx, sdk.ObjectRef{GVK: corev1.SchemeGroupVersion.WithKind("Pod"), Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID})
					}
					if err != nil {
						t.Fatal(err)
					}
					if observed.Root() != rootRef || !reflect.DeepEqual(observed.Object(), apiRoot) {
						t.Fatal("definition publication changed the physical root or its full body")
					}
					consumerE2EPods(t, observed.Pods(), expectedBodies)
					if observed.Generic() != (component == "pods") || len(observed.MembershipIssues()) != 0 {
						t.Fatalf("wrong definition state or incomplete membership for %s", component)
					}
					memberships := observed.PodMemberships()
					if len(memberships) != len(expectedBodies) {
						t.Fatal("definition publication changed membership UID coverage")
					}
					for _, pod := range expectedBodies {
						member := memberships[pod.UID]
						if member.Component != component {
							t.Fatalf("component = %s, want %s", member.Component, component)
						}
						if component != "pods" {
							replica := pod.Labels["jobset.sigs.k8s.io/job-index"]
							instance := pod.Labels["jobset.sigs.k8s.io/replicatedjob-name"]
							assertLiveMembershipCoordinate(t, memberships, []string{string(pod.UID)}, component, instance, &replica)
						}
					}
					result = observed
				}
				return result
			}
			known := observe("replicatedjob")
			originalMemberships := known.PodMemberships()
			if err := registry.Replace(); err != nil {
				t.Fatal(err)
			}
			generic := observe("pods")
			genericMemberships := generic.PodMemberships()
			if err := registry.Replace(changed); err != nil {
				t.Fatal(err)
			}
			updated := observe("jobtemplate")
			updatedMemberships := updated.PodMemberships()
			if err := registry.Replace(definition); err != nil {
				t.Fatal(err)
			}
			restored := observe("replicatedjob")
			if !reflect.DeepEqual(originalMemberships, restored.PodMemberships()) {
				t.Fatal("restored definition changed classified memberships")
			}
			provider.failure = errors.New("definition source unavailable")
			failed, err := discoverer.Workload(ctx, rootRef)
			if !errors.Is(err, provider.failure) || failed != nil {
				t.Fatalf("provider failure became a successful generic/cached observation: %v", err)
			}
			provider.failure = nil
			observe("replicatedjob")
			if !reflect.DeepEqual(known.PodMemberships(), originalMemberships) || !reflect.DeepEqual(generic.PodMemberships(), genericMemberships) || !reflect.DeepEqual(updated.PodMemberships(), updatedMemberships) || !generic.Generic() || updated.Definition().Spec.StructureDefinition.ChildComponents[0].Name != "jobtemplate" {
				t.Fatal("later publications changed earlier observations")
			}
			for _, retained := range []*sdk.Workload{known, generic, updated} {
				consumerE2EPods(t, retained.Pods(), expectedBodies)
			}
		})
	}
	t.Log("real JobSet: known -> generic -> revised -> restored definitions preserve exact root/full Pods in direct/index/cache modes; provider failures and retained observations verified without cluster writes")
}
