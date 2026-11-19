// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/dsx-ai-factory/workload-map/dsx-workload-exporter/pkg/discovery"
	inventory "github.com/dsx-ai-factory/workload-map/dsx-workload-exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/dsx-workload-exporter/pkg/store"
	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

func TestStatusDistinguishesMissingReferenceFromUnreadableCollection(t *testing.T) {
	r := newRootLifetimeRuntime(t, Config{})
	w := rootLifetimeWatch(t, r, lifetimeDeploymentGVK, "", true)
	root := rootLifetimeObject(lifetimeDeploymentGVK, "ns", "test", "root")
	require.NoError(t, r.state.applyWatchEvent(w, root, false))
	require.NoError(t, r.resolveSelectedObjectRoot(t.Context(), objectJobFor(refOf(root))))
	definition, err := catalog.Get(lifetimeDeploymentGVK)
	require.NoError(t, err)
	definition.Spec.StructureDefinition.References = []api.Reference{{Name: "settings", APIVersion: "v1", Kind: "ConfigMap", Lookup: &api.LookupReference{NameExpression: "object.metadata.name"}}}
	definition.Spec.StructureDefinition.RootComponent.StatusDefinition = &api.StatusDefinition{StatusMappings: api.StatusMappings{
		Running: []api.StatusMatcher{{ByExpression: &api.ExpressionMatcher{Expression: `!("settings" in references)`}}},
	}}
	r.state.selectedDefinitions[lifetimeDeploymentGVK.GroupKind()] = selectedDefinition{Selection: inventory.Selection{Definition: definition, GVK: lifetimeDeploymentGVK}}
	require.NoError(t, r.reconcileWorkload(t.Context(), root.GetUID()))
	require.Equal(t, store.ReasonAPIUnavailable, r.state.deps.Publications.Workload(root.GetUID()).StatusError)
	cm := rootLifetimeWatch(t, r, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "", true)
	for _, tc := range []struct {
		name      string
		synced    bool
		sourceErr error
		want      string
	}{
		{"pending", false, nil, store.ReasonAPIUnavailable},
		{"forbidden", false, apierrors.NewForbidden(cm.key.Resource.GroupResource(), "", errors.New("denied")), store.ReasonPermissionDenied},
		{"collection missing", true, apierrors.NewNotFound(cm.key.Resource.GroupResource(), ""), store.ReasonAPIUnavailable},
		{"synchronized and empty", true, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm.ready = tc.synced
			r.state.setWatchHealth(cm, tc.sourceErr)
			require.NoError(t, r.reconcileWorkload(t.Context(), root.GetUID()))
			result := r.state.deps.Publications.Workload(root.GetUID())
			require.NotNil(t, result)
			require.Equal(t, tc.want, result.StatusError)
			require.Equal(t, tc.want == "", result.Record.HasStatus)
			require.Contains(t, result.Sources, cm.key)
			require.NotContains(t, result.CreationSources, cm.key)
			if tc.want == "" {
				require.Contains(t, result.Record.Phases, "Running")
			}
		})
	}
	if got := cm.dependents[root.GetUID()]; got != root.GetNamespace() {
		t.Fatalf("watched namespace = %q, want %q", got, root.GetNamespace())
	}
	cm.referenceOnly = true
	r.state.withdrawWorkload(root.GetUID())
	require.NotContains(t, r.state.watches, cm.key)
	require.NotContains(t, r.state.sources, cm.key)
	require.NotNil(t, r.state.watches[w.key])
}

func TestReferenceChangeDuringEvaluationRejectsPublication(t *testing.T) {
	r := newRootLifetimeRuntime(t, Config{})
	pods := rootLifetimeWatch(t, r, podGVK, "", true)
	root := rootLifetimeObject(podGVK, "team", "job", "root")
	require.NoError(t, r.state.applyWatchEvent(pods, root, false))
	drainRootLifetimeQueues(t, r)
	key := objectJobFor(refOf(root))
	path := []discovery.ObjectRef{refOf(root)}
	selection, err := r.state.lookupDefinition(podGVK)
	require.NoError(t, err)
	newObservation := func() *referenceReader {
		return &referenceReader{state: r.state, root: refOf(root), selection: selection}
	}
	workloadResult := *r.state.deps.Publications.Workload(root.GetUID())
	podResult := *r.state.deps.Publications.Pod(root.GetUID())
	cm := rootLifetimeWatch(t, r, schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, "", true)
	observed := newObservation()
	_, err = observed.Get(t.Context(), cm.gvk, "team", "settings")
	require.ErrorIs(t, err, references.ErrNotFound)

	other := rootLifetimeObject(cm.gvk, "other", "settings", "another")
	require.NoError(t, r.state.applyWatchEvent(cm, other, false))
	require.NoError(t, r.state.publishWorkload(observed, workloadResult))
	require.NoError(t, r.state.publishPod(path, observed, podResult, false))

	settings := rootLifetimeObject(cm.gvk, "team", "settings", "settings")
	require.NoError(t, r.state.applyWatchEvent(cm, settings, false))
	require.ErrorIs(t, r.state.publishWorkload(observed, workloadResult), discovery.ErrStale)
	require.ErrorIs(t, r.state.publishPod(path, observed, podResult, false), discovery.ErrStale)

	observed = newObservation()
	items, err := observed.List(t.Context(), cm.gvk, references.ListQuery{Namespace: "team", Selector: labels.Everything()})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, r.state.applyWatchEvent(cm, settings, true))
	require.ErrorIs(t, r.state.publishWorkload(observed, workloadResult), discovery.ErrStale)

	denied := apierrors.NewForbidden(cm.key.Resource.GroupResource(), "", errors.New("denied"))
	r.state.setWatchHealth(cm, denied)
	observed = newObservation()
	_, err = observed.Get(t.Context(), cm.gvk, "team", "settings")
	require.ErrorIs(t, err, denied)
	require.NoError(t, r.state.failPodEvaluation(key, path, errors.Join(discovery.ErrEvaluation, err), observed))
	require.Contains(t, r.state.deps.Publications.Pod(root.GetUID()).Sources, cm.key)

	r.state.setWatchHealth(cm, nil)
	require.ErrorIs(t, r.state.failPodEvaluation(key, path, denied, observed), discovery.ErrStale)
	require.ErrorIs(t, r.state.publishWorkload(observed, workloadResult), discovery.ErrStale)
}
