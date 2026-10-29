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
	client := r.state.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient)
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
	r.state.mu.Lock()
	require.Same(t, old, r.state.watches[key])
	require.NotNil(t, r.state.replacements[key])
	r.state.mu.Unlock()
	require.False(t, values.Capture().Sources[key].Synced)
	_, _, err = r.state.reader.read(refOf(root), true)
	require.ErrorIs(t, err, discovery.ErrPending)
	close(release)
	require.Eventually(t, func() bool {
		r.state.mu.Lock()
		defer r.state.mu.Unlock()
		return r.state.watches[key] != old && r.state.watches[key].ready && r.state.replacements[key] == nil
	}, 2*time.Second, time.Millisecond)
	full, _, err := r.state.reader.read(refOf(root), true)
	require.NoError(t, err)
	require.Equal(t, int64(3), full.Object["spec"].(map[string]any)["replicas"])
	require.NoError(t, r.resolveLeaf(context.Background(), inputFor(refOf(pod))))
	require.True(t, values.Pod(pod.GetUID()).HasValue)
	require.NoError(t, r.state.accept(old, root, true))
	full, _, err = r.state.reader.read(refOf(root), true)
	require.NoError(t, err)
	require.NotNil(t, full)
	r.state.mu.Lock()
	require.Len(t, r.state.watches, 2)
	require.Empty(t, r.state.replacements)
	r.state.mu.Unlock()
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
			mapper := &recordingMapper{RESTMapper: r.state.reader.clients.Mapper, err: errors.New("mapping unavailable")}
			r.state.reader.clients.Mapper = mapper
			if failed {
				_, err := r.state.ensureWatch(context.Background(), root.GroupVersionKind(), root.GetNamespace(), true)
				require.Error(t, err)
				require.Len(t, values.Capture().Failures, 1)
				mapper.calls, mapper.err = 0, nil
			}
			require.NoError(t, r.resolveLeaf(context.Background(), inputFor(refOf(pod))))
			require.True(t, values.Pod(pod.GetUID()).HasValue)
			require.Empty(t, values.Pod(pod.GetUID()).Error)
			require.Empty(t, r.state.watchFailures)
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
			r.state.reader.clients.Metadata.(*metadatafake.FakeMetadataClient).PrependReactor("list", "replicasets", func(clienttesting.Action) (bool, kruntime.Object, error) {
				return true, nil, apierrors.NewGenericServerResponse(code, "list", w.key.Resource.GroupResource(), "", "unsupported representation", 0, false)
			})
			r.state.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "replicasets", func(clienttesting.Action) (bool, kruntime.Object, error) {
				return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*object}}, nil
			})
			list, err := r.state.listWatch(context.Background(), w, metav1.ListOptions{})
			require.NoError(t, err)
			items := list.(*unstructured.UnstructuredList).Items
			require.Len(t, items, 1)
			require.NotContains(t, items[0].Object, "spec")
			require.NoError(t, (&reflectorStore{runtime: r.state, source: w}).Replace([]any{&items[0]}, "1"))
			metadata, err := r.state.reader.GetMetadata(context.Background(), refOf(object))
			require.NoError(t, err)
			require.Equal(t, object.GetUID(), metadata.GetUID())
		})
	}
}

func TestWatchFailureAndEOFRemainVisibleUntilReconnect(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	_, _, source, _ := seedPair(t, r)
	stream := watch.NewRaceFreeFake()
	r.state.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) { return true, stream, nil })
	monitored, err := r.state.watchResource(context.Background(), source, metav1.ListOptions{})
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
	r.state.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) { return true, replacement, nil })
	recovered, err := r.state.watchResource(context.Background(), source, metav1.ListOptions{})
	require.NoError(t, err)
	defer recovered.Stop()
	require.Empty(t, values.Capture().Sources[source.key].Error)
}

func TestMalformedWatchEventRejectsSourceAndClosesStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, values := simpleRuntime(t, nil)
		source := seedSource(t, r, deploymentKind, true)
		stream := watch.NewRaceFreeFake()
		counted := &countingStream{Interface: stream}
		r.state.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) {
			return true, counted, nil
		})
		monitored, err := r.state.watchResource(context.Background(), source, metav1.ListOptions{})
		require.NoError(t, err)
		defer monitored.Stop()
		stream.Add(&metav1.Status{})
		synctest.Wait()
		r.state.wg.Wait()
		_, open := <-monitored.ResultChan()
		require.False(t, open, "malformed events must not reach the reflector")
		require.Equal(t, int32(1), counted.stops.Load())
		require.EqualError(t, source.err, "unexpected source object *v1.Status")
		require.Equal(t, store.ReasonAPIUnavailable, values.Capture().Sources[source.key].Error)
	})
}

func TestWatchShutdownStopsSourceOnce(t *testing.T) {
	for _, blockedOn := range []string{"input", "output"} {
		for _, shutdown := range []string{"stop", "cancel", "stop then EOF", "cancel then EOF", "concurrent stop and cancel"} {
			t.Run(blockedOn+"/"+shutdown, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					r, values := simpleRuntime(t, nil)
					source := seedSource(t, r, deploymentKind, true)
					stream := watch.NewRaceFreeFake()
					counted := &countingStream{Interface: stream}
					r.state.reader.clients.Dynamic.(*dynamicfake.FakeDynamicClient).PrependWatchReactor("deployments", func(clienttesting.Action) (bool, watch.Interface, error) {
						return true, counted, nil
					})
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					monitored, err := r.state.watchResource(ctx, source, metav1.ListOptions{})
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
					case "stop then EOF":
						monitored.Stop()
						stream.Stop()
					case "cancel then EOF":
						cancel()
						stream.Stop()
					case "concurrent stop and cancel":
						for range 10 {
							go monitored.Stop()
							go cancel()
						}
					}
					synctest.Wait()
					r.state.wg.Wait()
					_, open := <-monitored.ResultChan()
					require.False(t, open)
					require.Equal(t, int32(1), counted.stops.Load())
					require.Empty(t, values.Capture().Sources[source.key].Error)
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
	r.state.started = true
	seedSource(t, r, podGVK, true)
	absent := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Missing"}
	r.state.config.RequiredKinds = []schema.GroupVersionKind{absent}
	_, err := r.state.ensureWatch(context.Background(), absent, "", true)
	require.Error(t, err)
	require.False(t, r.Ready())
	require.Len(t, values.Capture().Failures, 1)
	require.Equal(t, absent, values.Capture().Failures[0].Input.GVK)
}

func TestReadinessUsesInitialSynchronizationAndRequiredSourceErrors(t *testing.T) {
	r, _ := simpleRuntime(t, nil)
	r.state.started = true
	pods := seedSource(t, r, podGVK, true)
	supplemental := seedSource(t, r, deploymentKind, true)
	require.True(t, r.Ready())
	r.state.mu.Lock()
	r.state.setHealthLocked(supplemental, errors.New("unavailable"))
	r.state.mu.Unlock()
	require.True(t, r.Ready())
	r.state.config.RequiredKinds = []schema.GroupVersionKind{deploymentKind}
	require.False(t, r.Ready())
	r.state.config.RequiredKinds = nil
	pods.ready = false
	require.False(t, r.Ready())
	pods.ready = true
	require.True(t, r.Ready())
}

func TestEmptyNamespaceSelectorUsesAllNamespacePodSource(t *testing.T) {
	r, _ := simpleRuntime(t, nil)
	other, err := New(Config{NamespaceSelector: &metav1.LabelSelector{}}, r.state.deps, r.state.reader)
	require.NoError(t, err)
	defer other.state.inputQueue.ShutDown()
	defer other.state.rootQueue.ShutDown()
	defer other.state.controlQueue.ShutDown()
	require.Nil(t, other.state.config.NamespaceSelector)
	require.True(t, other.state.scopes[""])
	require.False(t, other.state.requiredWatch(namespaceGVK))
}

func TestConflictingAPIAliasCannotRestoreDemotedRoot(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	root, pod, _, _ := seedPair(t, r)
	alternate := deploymentKind
	alternate.Version = "v2"
	gvr := alternate.GroupVersion().WithResource("deployments")
	r.state.reader.clients.Mapper.(*meta.DefaultRESTMapper).AddSpecific(alternate, gvr, gvr, meta.RESTScopeNamespace)
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
	require.NoError(t, r.state.accept(jobs, changed, false))
	require.Error(t, r.admitRoot(context.Background(), inputFor(refOf(changed))))
	require.NoError(t, r.reconcile(context.Background(), parent.GetUID()))
	require.Equal(t, store.ReasonUnresolvedOwner, values.Workload(parent.GetUID()).Error)
	require.NoError(t, r.admitRoot(context.Background(), inputFor(refOf(second))))
	require.NoError(t, r.reconcile(context.Background(), parent.GetUID()))
	require.Equal(t, store.ReasonUnresolvedOwner, values.Workload(parent.GetUID()).Error)
	require.NoError(t, r.state.accept(jobs, changed, true))
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

func TestUnselectedPodEventsKeepCacheWithoutLeafWork(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.state.podSelector, _ = selector(&metav1.LabelSelector{MatchLabels: map[string]string{"selected": "true"}})
	pods := seedSource(t, r, podGVK, true)
	initial := simpleObject(podGVK, "initial")
	require.NoError(t, (&reflectorStore{runtime: r.state, source: pods}).Replace([]any{initial}, "1"))
	require.Equal(t, 1, r.state.inputQueue.Len())
	key, _ := r.state.inputQueue.Get()
	require.True(t, key.Rescan, "initial synchronization queues only its scope rescan")
	require.NoError(t, r.resolveInput(context.Background(), key))
	r.state.inputQueue.Done(key)
	require.Zero(t, r.state.inputQueue.Len())
	_, cached := r.state.reader.Cached(refOf(initial))
	require.True(t, cached)

	added := simpleObject(podGVK, "added")
	require.NoError(t, r.state.accept(pods, added, false))
	require.Zero(t, r.state.inputQueue.Len())
	changed := added.DeepCopy()
	changed.SetResourceVersion("2")
	changed.Object["status"] = map[string]any{"phase": "Running"}
	require.NoError(t, r.state.accept(pods, changed, false))
	require.Zero(t, r.state.inputQueue.Len())
	current, cached := r.state.reader.Cached(refOf(changed))
	require.True(t, cached)
	require.Equal(t, changed.Object, current.Object)
	require.Empty(t, values.Capture().Pods)
	require.Empty(t, values.Capture().Workloads)
}

func TestPodSelectorTransitionsRemovePreviousResultsAndRestoreMapping(t *testing.T) {
	for _, previous := range []string{"mapping", "error only"} {
		t.Run(previous, func(t *testing.T) {
			r, values := simpleRuntime(t, nil)
			r.state.podSelector, _ = selector(&metav1.LabelSelector{MatchLabels: map[string]string{"selected": "true"}})
			root, pod := simpleObject(deploymentKind, "root"), simpleObject(podGVK, "pod")
			pod.SetLabels(map[string]string{"selected": "true"})
			simpleOwner(pod, root)
			roots := seedSource(t, r, deploymentKind, true)
			pods := seedSource(t, r, podGVK, true, pod)
			if previous == "mapping" {
				require.NoError(t, r.state.accept(roots, root, false))
				settle(t, r)
				require.True(t, values.Pod(pod.GetUID()).HasValue)
			} else {
				require.Error(t, r.resolveLeaf(context.Background(), inputFor(refOf(pod))))
				require.NotNil(t, values.Pod(pod.GetUID()))
				require.False(t, values.Pod(pod.GetUID()).HasValue)
				require.Equal(t, store.ReasonUnresolvedOwner, values.Pod(pod.GetUID()).Error)
				require.NotContains(t, r.state.podRoots, pod.GetUID())
				emptyQueue(r.state.inputQueue)
				emptyQueue(r.state.rootQueue)
			}

			changed := pod.DeepCopy()
			changed.SetResourceVersion("2")
			changed.SetLabels(nil)
			require.NoError(t, r.state.accept(pods, changed, false))
			require.Equal(t, 1, r.state.inputQueue.Len())
			// A later event must still schedule cleanup if the first queued item was lost.
			emptyQueue(r.state.inputQueue)
			changed.SetResourceVersion("3")
			require.NoError(t, r.state.accept(pods, changed, false))
			require.Equal(t, 1, r.state.inputQueue.Len())
			settle(t, r)
			require.Nil(t, values.Pod(pod.GetUID()))
			require.Empty(t, values.Capture().Workloads)
			_, cached := r.state.reader.Cached(refOf(changed))
			require.True(t, cached)

			require.NoError(t, r.state.accept(roots, root, false))
			settle(t, r)
			changed.SetResourceVersion("4")
			changed.SetLabels(map[string]string{"selected": "true"})
			require.NoError(t, r.state.accept(pods, changed, false))
			require.Equal(t, 1, r.state.inputQueue.Len())
			settle(t, r)
			require.True(t, values.Pod(pod.GetUID()).HasValue)
			require.Equal(t, root.GetUID(), values.Pod(pod.GetUID()).Record.Workload.UID)
			require.Empty(t, values.Pod(pod.GetUID()).Error)
		})
	}
}

func TestPodSelectorFiltersScopeAndDefinitionRescans(t *testing.T) {
	for _, trigger := range []string{"scope rescan", "definition change"} {
		t.Run(trigger, func(t *testing.T) {
			r, values := simpleRuntime(t, nil)
			r.state.podSelector, _ = selector(&metav1.LabelSelector{MatchLabels: map[string]string{"selected": "true"}})
			root := simpleObject(jobKind, "root")
			root.Object["spec"] = map[string]any{"parallelism": int64(1)}
			selected, ignored := simpleObject(podGVK, "selected"), simpleObject(podGVK, "ignored")
			selected.SetLabels(map[string]string{"selected": "true"})
			simpleOwner(selected, root)
			simpleOwner(ignored, root)
			seedSource(t, r, jobKind, true, root)
			seedSource(t, r, podGVK, true, selected, ignored)
			settle(t, r)
			require.NotNil(t, values.Pod(selected.GetUID()))
			require.Nil(t, values.Pod(ignored.GetUID()))

			if trigger == "scope rescan" {
				require.NoError(t, r.resolveInput(context.Background(), inputKey{Object: objectKey{Namespace: "ns"}, Rescan: true}))
			} else {
				selectCatalog(t, r, "batch-job-v1")
			}
			require.Equal(t, 1, r.state.inputQueue.Len())
			key, _ := r.state.inputQueue.Get()
			require.False(t, key.Rescan)
			require.Equal(t, selected.GetUID(), key.UID)
			require.NoError(t, r.resolveInput(context.Background(), key))
			r.state.inputQueue.Done(key)
			settle(t, r)
			require.True(t, values.Pod(selected.GetUID()).HasValue)
			if trigger == "definition change" {
				require.Equal(t, "batch-job-v1", values.Pod(selected.GetUID()).Record.Karta)
			}
			require.Nil(t, values.Pod(ignored.GetUID()))
			_, cached := r.state.reader.Cached(refOf(ignored))
			require.True(t, cached)
		})
	}
}

func TestUnselectedAncestorPodOwnershipChangeUpdatesSelectedDescendant(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.state.podSelector, _ = selector(&metav1.LabelSelector{MatchLabels: map[string]string{"selected": "true"}})
	a, b := simpleObject(deploymentKind, "a"), simpleObject(deploymentKind, "b")
	seedSource(t, r, deploymentKind, true, a, b)
	leader, child := simpleObject(podGVK, "leader"), simpleObject(podGVK, "child")
	child.SetLabels(map[string]string{"selected": "true"})
	simpleOwner(leader, a)
	simpleOwner(child, leader)
	pods := seedSource(t, r, podGVK, true, leader, child)
	settle(t, r)
	require.Nil(t, values.Pod(leader.GetUID()))
	require.Equal(t, a.GetUID(), values.Pod(child.GetUID()).Record.Workload.UID)
	_, cached := r.state.reader.Cached(refOf(leader))
	require.True(t, cached)

	changed := leader.DeepCopy()
	changed.SetResourceVersion("2")
	simpleOwner(changed, b)
	require.NoError(t, r.state.accept(pods, changed, false))
	require.Equal(t, 1, r.state.inputQueue.Len())
	key, _ := r.state.inputQueue.Get()
	require.True(t, key.Rescan)
	require.NoError(t, r.resolveInput(context.Background(), key))
	r.state.inputQueue.Done(key)
	require.Equal(t, 1, r.state.inputQueue.Len())
	settle(t, r)
	require.Nil(t, values.Pod(leader.GetUID()))
	require.Equal(t, b.GetUID(), values.Pod(child.GetUID()).Record.Workload.UID)
	require.Nil(t, values.Workload(a.GetUID()))
	require.NotNil(t, values.Workload(b.GetUID()))
	current, cached := r.state.reader.Cached(refOf(changed))
	require.True(t, cached)
	require.Equal(t, changed.GetOwnerReferences(), current.GetOwnerReferences())
}

func TestPodSelectorLossDuringDiscoveryQueuesCleanupBeforePublication(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.state.podSelector, _ = selector(&metav1.LabelSelector{MatchLabels: map[string]string{"selected": "true"}})
	pod := simpleObject(podGVK, "pod")
	pod.SetLabels(map[string]string{"selected": "true"})
	pods := seedSource(t, r, podGVK, true, pod)
	entered, resume := make(chan struct{}), make(chan struct{})
	discoverer := discovery.NewMockDiscovery(t)
	discoverer.EXPECT().Get(context.Background(), refOf(pod)).RunAndReturn(func(context.Context, discovery.ObjectRef) (discovery.Result, error) {
		close(entered)
		<-resume
		return discovery.Result{}, discovery.ErrUnavailable
	}).Once()
	r.state.deps.Discovery = discoverer
	key, _ := r.state.inputQueue.Get()
	done := make(chan error, 1)
	go func() { done <- r.resolveInput(context.Background(), key) }()
	<-entered
	require.Nil(t, values.Pod(pod.GetUID()))
	require.NotContains(t, r.state.podRoots, pod.GetUID())
	changed := pod.DeepCopy()
	changed.SetResourceVersion("2")
	changed.SetLabels(nil)
	require.NoError(t, r.state.accept(pods, changed, false))
	close(resume)
	require.ErrorIs(t, <-done, discovery.ErrUnavailable)
	r.state.inputQueue.Done(key)
	require.Equal(t, 1, r.state.inputQueue.Len())
	settle(t, r)
	require.Nil(t, values.Pod(pod.GetUID()))
	require.Empty(t, values.Capture().Workloads)
}

func TestOrdinaryPodChurnDoesNotRescanAndAncestorPodAdoptionDoes(t *testing.T) {
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
	require.NoError(t, r.state.accept(pods, ordinary, false))
	key, _ := r.state.inputQueue.Get()
	require.False(t, key.Rescan)
	require.Equal(t, ordinary.GetUID(), key.UID)
	r.state.inputQueue.Done(key)
	require.Zero(t, r.state.inputQueue.Len())
	require.NoError(t, r.state.accept(pods, ordinary, true))
	key, _ = r.state.inputQueue.Get()
	require.False(t, key.Rescan)
	r.state.inputQueue.Done(key)
	require.Zero(t, r.state.inputQueue.Len())
	adopted := leader.DeepCopy()
	adopted.SetResourceVersion("2")
	simpleOwner(adopted, b)
	require.NoError(t, r.state.accept(pods, adopted, false))
	settle(t, r)
	require.Equal(t, b.GetUID(), values.Pod(child.GetUID()).Record.Workload.UID)
	require.NoError(t, r.state.accept(pods, adopted, true))
	foundRescan := false
	for r.state.inputQueue.Len() > 0 {
		key, _ := r.state.inputQueue.Get()
		foundRescan = foundRescan || key.Rescan
		r.state.inputQueue.Done(key)
	}
	require.True(t, foundRescan)
	require.NotContains(t, r.state.ancestorPods, leader.GetUID())
}

func TestFailedWatchRequestsRetireWithNamespace(t *testing.T) {
	r, values := simpleRuntime(t, nil)
	r.state.config.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"include": "yes"}}
	r.state.namespaceSelector, _ = selector(r.state.config.NamespaceSelector)
	r.state.scopes = map[string]bool{"ns": true}
	seedSource(t, r, namespaceGVK, true)
	missing := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Missing"}
	_, err := r.state.ensureWatch(context.Background(), missing, "ns", true)
	require.Error(t, err)
	require.NotEmpty(t, values.Capture().Failures)
	require.NoError(t, r.state.syncWatches(context.Background()))
	require.Empty(t, r.state.watchFailures)
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
	r.state.config.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"include": "yes"}}
	r.state.namespaceSelector, _ = selector(r.state.config.NamespaceSelector)
	r.state.scopes = map[string]bool{"ns": true}
	seedSource(t, r, namespaceGVK, true)
	missing := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Missing"}
	mapper := &blockedMapper{RESTMapper: r.state.reader.clients.Mapper, blocked: missing.GroupKind(), entered: make(chan struct{}), resume: make(chan struct{})}
	r.state.reader.clients.Mapper = mapper
	done := make(chan error, 1)
	go func() { _, err := r.state.ensureWatch(context.Background(), missing, "ns", true); done <- err }()
	<-mapper.entered
	require.NoError(t, r.state.syncWatches(context.Background()))
	close(mapper.resume)
	require.ErrorIs(t, <-done, discovery.ErrPending)
	require.Empty(t, r.state.watchFailures)
	require.Empty(t, values.Capture().Failures)
}
