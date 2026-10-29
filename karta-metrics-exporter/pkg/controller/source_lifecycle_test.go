// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/store"
)

type countingStream struct {
	watch.Interface
	stops atomic.Int32
}

func (s *countingStream) Stop() {
	s.stops.Add(1)
	s.Interface.Stop()
}

func TestSourcePromotionWaitsForFullSnapshotAndRetiresMetadata(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	root := simpleObject(deploymentKind, "root")
	root.Object["spec"] = map[string]any{"replicas": int64(3)}
	old := seedSource(t, r, deploymentKind, false, root)
	pod := simpleObject(podGVK, "pod")
	simpleOwner(pod, root)
	seedSource(t, r, podGVK, true, pod)
	fullList := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	client := r.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient)
	client.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, kruntime.Object, error) {
		once.Do(func() { close(fullList) })
		<-release
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*root}}
		list.SetResourceVersion("5")
		return true, list, nil
	})
	client.PrependWatchReactor("deployments", func(action clienttesting.Action) (bool, watch.Interface, error) {
		options := action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions()
		if options.SendInitialEvents != nil && *options.SendInitialEvents {
			return true, nil, apierrors.NewBadRequest("optional watch-list unsupported")
		}
		return true, watch.NewRaceFreeFake(), nil
	})
	err := r.resolveLeaf(context.Background(), inputFor(refOf(pod)))
	require.ErrorIs(t, err, discovery.ErrPending)
	key := old.key
	select {
	case <-fullList:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("standby LIST did not start")
	}
	r.mu.Lock()
	require.Same(t, old, r.watches[key])
	require.NotNil(t, r.replacements[key])
	r.mu.Unlock()
	require.False(t, values.Capture().Sources[key].Synced)
	_, _, err = r.reader.read(refOf(root), true)
	require.ErrorIs(t, err, discovery.ErrPending)
	close(release)
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.watches[key] != old && r.watches[key].ready && r.replacements[key] == nil
	}, 2*time.Second, time.Millisecond)
	full, _, err := r.reader.read(refOf(root), true)
	require.NoError(t, err)
	require.Equal(t, int64(3), full.Object["spec"].(map[string]any)["replicas"])
	require.NoError(t, r.resolveLeaf(context.Background(), inputFor(refOf(pod))))
	require.True(t, values.Pod(pod.GetUID()).HasValue)
	require.NoError(t, r.accept(old, root, true))
	full, _, err = r.reader.read(refOf(root), true)
	require.NoError(t, err)
	require.NotNil(t, full)
	r.mu.Lock()
	require.Len(t, r.watches, 2)
	require.Empty(t, r.replacements)
	r.mu.Unlock()
}

type recordingMapper struct {
	meta.RESTMapper
	err   error
	calls int
}

func (m *recordingMapper) RESTMapping(kind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.RESTMapper.RESTMapping(kind, versions...)
}

func TestPodUsesFullRootCacheAndRecoversMappingFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy cache", true: "previous mapping failure"}[failed], func(t *testing.T) {
			r, values := simpleRuntime(t, nil)
			root, pod, _, _ := seedPair(t, r)
			mapper := &recordingMapper{RESTMapper: r.reader.clients.Mapper, err: errors.New("mapping unavailable")}
			r.reader.clients.Mapper = mapper
			if failed {
				_, err := r.ensureWatch(context.Background(), root.GroupVersionKind(), root.GetNamespace(), true)
				require.Error(t, err)
				require.Len(t, values.Capture().Failures, 1)
				mapper.calls, mapper.err = 0, nil
			}
			require.NoError(t, r.resolveLeaf(context.Background(), inputFor(refOf(pod))))
			require.True(t, values.Pod(pod.GetUID()).HasValue)
			require.Empty(t, values.Pod(pod.GetUID()).Error)
			require.Empty(t, r.watchFailures)
			require.Empty(t, values.Capture().Failures)
			if failed {
				require.Equal(t, 1, mapper.calls)
			} else {
				require.Zero(t, mapper.calls)
			}
		})
	}
}

func TestMetadataNegotiationFallbackPreservesMetadataCache(t *testing.T) {
	for _, code := range []int{406, 415} {
		t.Run(string(rune(code)), func(t *testing.T) {
			r, _ := simpleRuntime(t, nil)
			object := simpleObject(replicaSetKind, "middle")
			object.Object["spec"] = map[string]any{"replicas": int64(3)}
			w := seedSource(t, r, replicaSetKind, false)
			r.reader.clients.Metadata.(*metadatafake.FakeMetadataClient).PrependReactor("list", "replicasets", func(clienttesting.Action) (bool, kruntime.Object, error) {
				return true, nil, apierrors.NewGenericServerResponse(code, "list", w.key.Resource.GroupResource(), "", "unsupported representation", 0, false)
			})
			r.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "replicasets", func(clienttesting.Action) (bool, kruntime.Object, error) {
				return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*object}}, nil
			})
			list, err := r.listWatch(context.Background(), w, metav1.ListOptions{})
			require.NoError(t, err)
			items := list.(*unstructured.UnstructuredList).Items
			require.Len(t, items, 1)
			require.NotContains(t, items[0].Object, "spec")
			require.NoError(t, (&reflectorStore{runtime: r, source: w}).Replace([]any{&items[0]}, "1"))
			metadata, err := r.reader.GetMetadata(context.Background(), refOf(object))
			require.NoError(t, err)
			require.Equal(t, object.GetUID(), metadata.GetUID())
		})
	}
}

func TestWatchFailureAndEOFRemainVisibleUntilReconnect(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	_, _, source, _ := seedPair(t, r)
	stream := watch.NewRaceFreeFake()
	r.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) { return true, stream, nil })
	monitored, err := r.watchResource(context.Background(), source, metav1.ListOptions{})
	require.NoError(t, err)
	defer monitored.Stop()
	denied := apierrors.NewForbidden(source.key.Resource.GroupResource(), "", errors.New("denied"))
	stream.Error(&metav1.Status{Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden, Message: denied.Error()})
	select {
	case <-monitored.ResultChan():
	case <-time.After(time.Second):
		t.Fatal("watch error not delivered")
	}
	require.Equal(t, store.ReasonPermissionDenied, values.Capture().Sources[source.key].Error)
	stream.Stop()
	require.Eventually(t, func() bool { return values.Capture().Sources[source.key].Error == store.ReasonAPIUnavailable }, time.Second, time.Millisecond)
	replacement := watch.NewRaceFreeFake()
	r.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) { return true, replacement, nil })
	recovered, err := r.watchResource(context.Background(), source, metav1.ListOptions{})
	require.NoError(t, err)
	defer recovered.Stop()
	require.Empty(t, values.Capture().Sources[source.key].Error)
}

func TestWatchShutdownStopsSourceOnce(t *testing.T) {
	for _, blockedOn := range []string{"input", "output"} {
		for _, shutdown := range []string{"stop", "cancel", "concurrent stop and cancel"} {
			t.Run(blockedOn+"/"+shutdown, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					r, _ := simpleRuntime(t, nil)
					source := seedSource(t, r, deploymentKind, true)
					stream := watch.NewRaceFreeFake()
					counted := &countingStream{Interface: stream}
					r.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) {
						return true, counted, nil
					})
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					monitored, err := r.watchResource(ctx, source, metav1.ListOptions{})
					require.NoError(t, err)
					defer monitored.Stop()
					if blockedOn == "output" {
						stream.Add(simpleObject(deploymentKind, "root"))
					}
					synctest.Wait()
					require.Empty(t, stream.ResultChan())
					require.Zero(t, counted.stops.Load())
					switch shutdown {
					case "stop":
						monitored.Stop()
					case "cancel":
						cancel()
					case "concurrent stop and cancel":
						for range 10 {
							go monitored.Stop()
							go cancel()
						}
					}
					synctest.Wait()
					r.wg.Wait()
					_, open := <-monitored.ResultChan()
					require.False(t, open)
					require.Equal(t, int32(1), counted.stops.Load())
					monitored.Stop()
					cancel()
					require.Equal(t, int32(1), counted.stops.Load())
				})
			})
		}
	}
}

func TestRequiredMappingFailureIsVisibleAndBlocksReadiness(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.started = true
	seedSource(t, r, podGVK, true)
	absent := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Missing"}
	r.config.RequiredKinds = []schema.GroupVersionKind{absent}
	_, err := r.ensureWatch(context.Background(), absent, "", true)
	require.Error(t, err)
	require.False(t, r.Ready())
	require.Len(t, values.Capture().Failures, 1)
	require.Equal(t, absent, values.Capture().Failures[0].Input.GVK)
}

func TestReadinessUsesInitialSynchronizationAndRequiredSourceErrors(t *testing.T) {
	r, _ := simpleRuntime(t, nil)
	r.started = true
	pods := seedSource(t, r, podGVK, true)
	supplemental := seedSource(t, r, deploymentKind, true)
	require.True(t, r.Ready())
	r.mu.Lock()
	r.setHealthLocked(supplemental, errors.New("unavailable"))
	r.mu.Unlock()
	require.True(t, r.Ready())
	r.config.RequiredKinds = []schema.GroupVersionKind{deploymentKind}
	require.False(t, r.Ready())
	r.config.RequiredKinds = nil
	pods.ready = false
	require.False(t, r.Ready())
	pods.ready = true
	require.True(t, r.Ready())
}

func TestEmptyNamespaceSelectorUsesAllNamespacePodSource(t *testing.T) {
	r, _ := simpleRuntime(t, nil)
	other, err := New(Config{NamespaceSelector: &metav1.LabelSelector{}}, r.deps, r.reader)
	require.NoError(t, err)
	defer other.inputQueue.ShutDown()
	defer other.rootQueue.ShutDown()
	defer other.controlQueue.ShutDown()
	require.Nil(t, other.config.NamespaceSelector)
	require.True(t, other.scopes[""])
	require.False(t, other.requiredWatch(namespaceGVK))
}

func TestConflictingAPIAliasCannotRestoreDemotedRoot(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	root, pod, _, _ := seedPair(t, r)
	alternate := deploymentKind
	alternate.Version = "v2"
	gvr := alternate.GroupVersion().WithResource("deployments")
	r.reader.clients.Mapper.(*meta.DefaultRESTMapper).AddSpecific(alternate, gvr, gvr, meta.RESTScopeNamespace)
	alias := root.DeepCopy()
	alias.SetGroupVersionKind(alternate)
	parent := simpleObject(deploymentKind, "missing")
	simpleOwner(alias, parent)
	alias.SetResourceVersion("2")
	seedSource(t, r, alternate, true, alias)
	require.Error(t, r.reconcile(context.Background(), root.GetUID()))
	require.Equal(t, store.ReasonUnresolvedOwner, values.Workload(root.GetUID()).Error)
	require.Error(t, r.resolveLeaf(context.Background(), inputFor(refOf(pod))))
	require.Equal(t, store.ReasonUnresolvedOwner, values.Pod(pod.GetUID()).Error)
	require.Len(t, values.Capture().Workloads, 1, "different API aliases share one canonical writer")
}

func TestKnownCandidateFailureInvalidatesRootWithoutOtherCandidatesClearingIt(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	selectCatalog(t, r, "batch-job-v1")
	parent := simpleObject(deploymentKind, "parent")
	first, second := simpleObject(jobKind, "first"), simpleObject(jobKind, "second")
	simpleOwner(first, parent)
	simpleOwner(second, parent)
	seedSource(t, r, deploymentKind, true, parent)
	jobs := seedSource(t, r, jobKind, true, first, second)
	seedSource(t, r, podGVK, true)
	settle(t, r)
	missing := simpleObject(replicaSetKind, "missing")
	seedSource(t, r, replicaSetKind, false)
	changed := first.DeepCopy()
	changed.SetResourceVersion("2")
	simpleOwner(changed, missing)
	require.NoError(t, r.accept(jobs, changed, false))
	require.Error(t, r.admitRoot(context.Background(), inputFor(refOf(changed))))
	require.NoError(t, r.reconcile(context.Background(), parent.GetUID()))
	require.Equal(t, store.ReasonUnresolvedOwner, values.Workload(parent.GetUID()).Error)
	require.NoError(t, r.admitRoot(context.Background(), inputFor(refOf(second))))
	require.NoError(t, r.reconcile(context.Background(), parent.GetUID()))
	require.Equal(t, store.ReasonUnresolvedOwner, values.Workload(parent.GetUID()).Error)
	require.NoError(t, r.accept(jobs, changed, true))
	require.NoError(t, r.admitRoot(context.Background(), inputFor(refOf(changed))))
	require.NoError(t, r.reconcile(context.Background(), parent.GetUID()))
	require.Empty(t, values.Workload(parent.GetUID()).Error)
}

func TestInitialLabelErrorKeepsBasePodMapping(t *testing.T) {
	r, values := simpleRuntime(t, &controlledLabels{fail: true})
	root, pod, _, _ := seedPair(t, r)
	row := values.Pod(pod.GetUID())
	require.True(t, row.HasValue)
	require.Equal(t, root.GetUID(), row.Record.Workload.UID)
	require.Equal(t, store.ReasonEvaluationFailed, row.Error)
	require.Equal(t, "<unknown>", row.Record.Component)
}

func TestOrdinaryPodChurnDoesNotSweepAndAncestorPodAdoptionDoes(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	a, b := simpleObject(deploymentKind, "a"), simpleObject(deploymentKind, "b")
	seedSource(t, r, deploymentKind, true, a, b)
	leader, child := simpleObject(podGVK, "leader"), simpleObject(podGVK, "child")
	simpleOwner(leader, a)
	simpleOwner(child, leader)
	pods := seedSource(t, r, podGVK, true, leader, child)
	settle(t, r)
	ordinary := simpleObject(podGVK, "ordinary")
	simpleOwner(ordinary, a)
	require.NoError(t, r.accept(pods, ordinary, false))
	key, _ := r.inputQueue.Get()
	require.False(t, key.Sweep)
	require.Equal(t, ordinary.GetUID(), key.UID)
	r.inputQueue.Done(key)
	require.Zero(t, r.inputQueue.Len())
	require.NoError(t, r.accept(pods, ordinary, true))
	key, _ = r.inputQueue.Get()
	require.False(t, key.Sweep)
	r.inputQueue.Done(key)
	require.Zero(t, r.inputQueue.Len())
	adopted := leader.DeepCopy()
	adopted.SetResourceVersion("2")
	simpleOwner(adopted, b)
	require.NoError(t, r.accept(pods, adopted, false))
	settle(t, r)
	require.Equal(t, b.GetUID(), values.Pod(child.GetUID()).Record.Workload.UID)
	require.NoError(t, r.accept(pods, adopted, true))
	foundSweep := false
	for r.inputQueue.Len() > 0 {
		key, _ := r.inputQueue.Get()
		foundSweep = foundSweep || key.Sweep
		r.inputQueue.Done(key)
	}
	require.True(t, foundSweep)
	require.NotContains(t, r.ancestorPods, leader.GetUID())
}

func TestFailedWatchRequestsRetireWithNamespace(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.config.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"include": "yes"}}
	r.namespaceSelector, _ = selector(r.config.NamespaceSelector)
	r.scopes = map[string]bool{"ns": true}
	seedSource(t, r, namespaceGVK, true)
	missing := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Missing"}
	_, err := r.ensureWatch(context.Background(), missing, "ns", true)
	require.Error(t, err)
	require.NotEmpty(t, values.Capture().Failures)
	require.NoError(t, r.syncWatches(context.Background()))
	require.Empty(t, r.watchFailures)
	require.Empty(t, values.Capture().Failures)
}

type blockedMapper struct {
	meta.RESTMapper
	blocked         schema.GroupKind
	entered, resume chan struct{}
}

func (m *blockedMapper) RESTMapping(kind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if kind == m.blocked {
		close(m.entered)
		<-m.resume
		return nil, &meta.NoKindMatchError{GroupKind: kind, SearchedVersions: versions}
	}
	return m.RESTMapper.RESTMapping(kind, versions...)
}
func TestLateMappingFailureCannotReviveRemovedNamespace(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.config.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"include": "yes"}}
	r.namespaceSelector, _ = selector(r.config.NamespaceSelector)
	r.scopes = map[string]bool{"ns": true}
	seedSource(t, r, namespaceGVK, true)
	missing := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Missing"}
	mapper := &blockedMapper{RESTMapper: r.reader.clients.Mapper, blocked: missing.GroupKind(), entered: make(chan struct{}), resume: make(chan struct{})}
	r.reader.clients.Mapper = mapper
	done := make(chan error, 1)
	go func() { _, err := r.ensureWatch(context.Background(), missing, "ns", true); done <- err }()
	<-mapper.entered
	require.NoError(t, r.syncWatches(context.Background()))
	close(mapper.resume)
	require.ErrorIs(t, <-done, discovery.ErrPending)
	require.Empty(t, r.watchFailures)
	require.Empty(t, values.Capture().Failures)
}
