// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build live_discovery

package discovery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	kdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	sdk "github.com/dsx-ai-factory/workload-map/pkg/discovery"
	sdkclient "github.com/dsx-ai-factory/workload-map/pkg/discovery/clientgo"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

type identity struct {
	Namespace, Name, UID, APIVersion, Kind string
	Owners                                 []metav1.OwnerReference
}

func (i identity) ref() sdk.ObjectRef {
	gv, err := schema.ParseGroupVersion(i.APIVersion)
	if err != nil {
		panic(err)
	}
	return sdk.ObjectRef{Namespace: i.Namespace, Name: i.Name, UID: types.UID(i.UID), GVK: gv.WithKind(i.Kind)}
}

type semantic struct {
	Name       string
	PodUIDs    []string
	Instances  []semantic
	Replicas   []semantic
	Components []semantic
	Key        string
}
type oracle struct {
	Namespace string
	PodCount  int
	Workloads []struct {
		Root               identity
		PodUIDs            []string
		SemanticComponents []semantic
	}
	APIObjects []identity
}

type countingOwnerMetadataReader struct {
	sdk.OwnerMetadataReader
	ownerReads int
}

func liveClient(t *testing.T) (context.Context, *rest.Config, dynamic.Interface, meta.RESTMapper, sdk.Reader) {
	t.Helper()
	path := os.Getenv("KARTA_DISCOVERY_KUBECONFIG")
	if path == "" {
		t.Skip("set KARTA_DISCOVERY_KUBECONFIG to the dedicated kind kubeconfig")
	}
	selected := os.Getenv("KARTA_DISCOVERY_CONTEXT")
	if selected == "" {
		selected = "kind-karta-sdk-discovery"
	}
	if !strings.HasPrefix(selected, "kind-") {
		t.Fatal("live discovery tests require an explicit kind context")
	}
	loading := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: selected}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.QPS = 100
	config.Burst = 200
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	discoveryClient, err := kdiscovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))
	reader, err := sdkclient.NewReader(client, mapper)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx, config, client, mapper, reader
}

func assertPods(t *testing.T, pods []*corev1.Pod, expected []string) {
	t.Helper()
	actual := make([]string, len(pods))
	for i, pod := range pods {
		actual[i] = string(pod.UID)
	}
	slices.Sort(actual)
	want := slices.Clone(expected)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("Pod UID set: got %v want %v", actual, want)
	}
}

func (r *countingOwnerMetadataReader) GetOwnerMetadata(ctx context.Context, ref sdk.ObjectRef) (*metav1.PartialObjectMetadata, error) {
	r.ownerReads++
	return r.OwnerMetadataReader.GetOwnerMetadata(ctx, ref)
}

func (r *countingOwnerMetadataReader) ListPodsForTransfer(ctx context.Context, namespace string) (*corev1.PodList, error) {
	return r.OwnerMetadataReader.(sdk.PodTransferReader).ListPodsForTransfer(ctx, namespace)
}

func assertLiveOwnershipPath(t *testing.T, path []sdk.ObjectRef, pod, root sdk.ObjectRef, objects map[types.UID]identity) {
	t.Helper()
	if len(path) == 0 || path[0] != pod || path[len(path)-1] != root {
		t.Fatalf("ownership path endpoints: got %+v, want %v through %v", path, pod, root)
	}
	kinds := make([]string, len(path))
	seen := make(map[types.UID]bool, len(path))
	for i, ref := range path {
		object, ok := objects[ref.UID]
		if !ok || object.ref() != ref || seen[ref.UID] {
			t.Fatalf("path entry %d is not a unique oracle identity: %+v", i, ref)
		}
		seen[ref.UID] = true
		kinds[i] = ref.GVK.Kind
		var controller *metav1.OwnerReference
		for _, owner := range object.Owners {
			if owner.Controller != nil && *owner.Controller {
				if controller != nil {
					t.Fatalf("oracle object %s has multiple controller owners", object.Name)
				}
				controller = &owner
			}
		}
		if i == len(path)-1 {
			if controller != nil {
				t.Fatalf("path stopped before oracle controller %s of %s", controller.Name, object.Name)
			}
			continue
		}
		next := path[i+1]
		if controller == nil || controller.UID != next.UID || controller.Name != next.Name || controller.Kind != next.GVK.Kind || controller.APIVersion != next.GVK.GroupVersion().String() {
			t.Fatalf("path edge %v -> %v disagrees with oracle controller %+v", ref, next, controller)
		}
	}
	switch root.GVK.Kind {
	case "JobSet":
		if !slices.Equal(kinds, []string{"Pod", "Job", "JobSet"}) {
			t.Fatalf("JobSet path omitted its Job: %v", kinds)
		}
	case "Deployment":
		if !slices.Equal(kinds, []string{"Pod", "ReplicaSet", "Deployment"}) {
			t.Fatalf("Deployment path omitted its ReplicaSet: %v", kinds)
		}
	}
}

func assertLivePodMemberships(t *testing.T, workload *sdk.Workload, expected []string) map[types.UID]attribution.Membership {
	t.Helper()
	memberships := workload.PodMemberships()
	if len(memberships) != len(expected) {
		t.Fatalf("PodMemberships has %d entries for %d oracle Pods", len(memberships), len(expected))
	}
	rootComponent := strings.ToLower(workload.Root().GVK.Kind)
	pathLength := 1
	switch workload.Root().GVK.Kind {
	case "Bundle":
		rootComponent = "pods"
	case "JobSet":
		pathLength = 2
	case "LeaderWorkerSet":
		pathLength = 3
	}
	for _, uid := range expected {
		membership, ok := memberships[types.UID(uid)]
		if !ok || len(membership.Issues) != 0 || len(membership.Path) != pathLength {
			t.Fatalf("Pod %s membership: %+v, found=%t; want %d complete coordinates", uid, membership, ok, pathLength)
		}
		first := membership.Path[0]
		if first.Component != rootComponent || first.Instance == nil || *first.Instance != "" || first.Replica != nil {
			t.Fatalf("Pod %s root coordinate: %+v, want anonymous %s without a replica", uid, first, rootComponent)
		}
		if membership.Component == "" || membership.Path[len(membership.Path)-1].Component != membership.Component {
			t.Fatalf("Pod %s membership does not end at its selected component: %+v", uid, membership)
		}
	}
	return memberships
}

func assertLiveMembershipCoordinate(t *testing.T, memberships map[types.UID]attribution.Membership, podUIDs []string, component, instance string, replica *string) {
	t.Helper()
	for _, uid := range podUIDs {
		membership, ok := memberships[types.UID(uid)]
		if !ok {
			t.Fatalf("oracle Pod %s has no membership", uid)
		}
		found := false
		for _, coordinate := range membership.Path {
			if coordinate.Component != component {
				continue
			}
			if found || coordinate.Instance == nil || *coordinate.Instance != instance || !reflect.DeepEqual(coordinate.Replica, replica) {
				t.Fatalf("Pod %s coordinate %+v does not match oracle component=%s instance=%q replica=%v", uid, coordinate, component, instance, replica)
			}
			found = true
		}
		if !found {
			t.Fatalf("Pod %s membership omits oracle component %s: %+v", uid, component, membership)
		}
	}
}

func TestLiveOperatorMembership(t *testing.T) {
	ctx, config, client, mapper, reader := liveClient(t)
	metadataClient, err := metadata.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	metadataReader, err := sdkclient.NewReaderWithOwnerMetadata(client, metadataClient, mapper)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := metadataReader.(sdk.PodTransferReader); !ok {
		t.Fatal("owner metadata adapter must retain Pod transfer")
	}
	counted := &countingOwnerMetadataReader{OwnerMetadataReader: metadataReader}
	countedPodREST := &countingOwnerMetadataReader{OwnerMetadataReader: metadataReader}
	coreScheme := runtime.NewScheme()
	if err := corev1.AddToScheme(coreScheme); err != nil {
		t.Fatal(err)
	}
	podConfig := rest.CopyConfig(config)
	podConfig.GroupVersion = &corev1.SchemeGroupVersion
	podConfig.APIPath = "/api"
	podConfig.ContentType = runtime.ContentTypeJSON
	podConfig.NegotiatedSerializer = serializer.NewCodecFactory(coreScheme).WithoutConversion()
	httpClient, err := rest.HTTPClientFor(podConfig)
	if err != nil {
		t.Fatal(err)
	}
	podREST, err := rest.RESTClientForConfigAndClient(podConfig, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	podReader, err := sdkclient.WithPodRESTClient(reader, podREST)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := podReader.(sdk.OwnerMetadataReader); ok {
		t.Fatal("plain Pod REST wrapper must not add owner metadata reads")
	}
	podMetadataReader, err := sdkclient.WithPodRESTClient(countedPodREST, podREST)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := podMetadataReader.(sdk.OwnerMetadataReader); !ok {
		t.Fatal("Pod REST wrapper must retain owner metadata reads")
	}
	for _, wrapped := range []sdk.Reader{podReader, podMetadataReader} {
		if _, ok := wrapped.(sdk.PodTransferReader); !ok {
			t.Fatal("Pod REST wrapper must retain Pod transfer")
		}
	}
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
	var roots []sdk.ObjectRef
	for _, workload := range expected.Workloads {
		roots = append(roots, workload.Root.ref())
	}
	dataset, err := sdk.Collect(ctx, reader, expected.Namespace, roots...)
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Pods) != expected.PodCount {
		t.Fatalf("collected %d Pods, expected %d", len(dataset.Pods), expected.PodCount)
	}
	index := sdk.NewIndex()
	if _, err := index.Replace(ctx, dataset); err != nil {
		t.Fatal(err)
	}
	podRefs := make(map[string]sdk.ObjectRef)
	oracleObjects := make(map[types.UID]identity, len(expected.APIObjects))
	for _, object := range expected.APIObjects {
		oracleObjects[types.UID(object.UID)] = object
		if object.Kind == "Pod" {
			podRefs[object.UID] = object.ref()
		}
	}
	for _, mode := range []struct {
		name    string
		reader  sdk.Reader
		options sdk.Options
	}{
		{name: "direct", reader: reader},
		{name: "direct-owner-metadata", reader: counted},
		{name: "direct-pod-rest", reader: podReader},
		{name: "direct-pod-rest-owner-metadata", reader: podMetadataReader},
		{name: "indexed", reader: index},
		{name: "indexed-attribution-cache", reader: index, options: sdk.Options{
			Cache: &sdk.CacheOptions{MaxWorkloads: 100, MaxPods: 1000},
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			resolver, err := karta.NewDiscoverer(mode.reader, mode.options)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range expected.Workloads {
				t.Run(item.Root.Kind+"/"+item.Root.Name, func(t *testing.T) {
					workload, err := resolver.Workload(ctx, item.Root.ref())
					if err != nil {
						t.Fatal(err)
					}
					if workload.Root() != item.Root.ref() {
						t.Fatalf("wrong root %v", workload.Root())
					}
					assertPods(t, workload.Pods(), item.PodUIDs)
					memberships := assertLivePodMemberships(t, workload, item.PodUIDs)
					if mode.options.Cache != nil {
						repeated, err := resolver.Workload(ctx, item.Root.ref())
						if err != nil {
							t.Fatal(err)
						}
						if repeated == workload || !reflect.DeepEqual(repeated.Object(), workload.Object()) || !reflect.DeepEqual(repeated.Pods(), workload.Pods()) {
							t.Fatal("repeated acquisition changed complete bodies or reused the observation handle")
						}
						if !reflect.DeepEqual(repeated.PodMemberships(), memberships) {
							t.Fatal("repeated acquisition changed captured Pod memberships")
						}
					}
					assertInterfaceConsumers(t, ctx, workload, len(item.PodUIDs))
					if len(workload.MembershipIssues()) != 0 {
						t.Fatalf("unexpected attribution issues: %v", workload.MembershipIssues())
					}
					if item.Root.Kind == "Bundle" {
						if !workload.Generic() {
							t.Fatal("unknown Bundle should use generic membership")
						}
						component, err := workload.Component("pods")
						if err != nil {
							t.Fatal(err)
						}
						pods, err := component.Pods()
						if err != nil {
							t.Fatal(err)
						}
						assertPods(t, pods, item.PodUIDs)
					}
					for _, podUID := range item.PodUIDs {
						path, err := resolver.OwnershipPath(ctx, podRefs[podUID])
						if err != nil {
							t.Fatal(err)
						}
						assertLiveOwnershipPath(t, path, podRefs[podUID], item.Root.ref(), oracleObjects)
						fromPod, err := resolver.WorkloadForPod(ctx, podRefs[podUID])
						if err != nil {
							t.Fatal(err)
						}
						if fromPod.Root() != item.Root.ref() {
							t.Fatalf("wrong root %v", fromPod.Root())
						}
						assertPods(t, fromPod.Pods(), item.PodUIDs)
						if !reflect.DeepEqual(fromPod.PodMemberships(), memberships) {
							t.Fatal("root and Pod queries returned different Pod memberships")
						}
					}
					for _, expectedComponent := range item.SemanticComponents {
						component, err := workload.Component(expectedComponent.Name)
						if err != nil {
							t.Fatal(err)
						}
						all, err := component.Pods()
						if err != nil {
							t.Fatal(err)
						}
						assertPods(t, all, item.PodUIDs)
						for _, expectedInstance := range expectedComponent.Instances {
							instance, err := component.Instance(expectedInstance.Name)
							if err != nil {
								t.Fatal(err)
							}
							if expectedInstance.PodUIDs != nil {
								pods, err := instance.Pods()
								if err != nil {
									t.Fatal(err)
								}
								assertPods(t, pods, expectedInstance.PodUIDs)
							}
							for _, expectedReplica := range expectedInstance.Replicas {
								replica, err := instance.Replica(expectedReplica.Key)
								if err != nil {
									t.Fatal(err)
								}
								pods, err := replica.Pods()
								if err != nil {
									t.Fatal(err)
								}
								assertPods(t, pods, expectedReplica.PodUIDs)
								assertLiveMembershipCoordinate(t, memberships, expectedReplica.PodUIDs, expectedComponent.Name, expectedInstance.Name, &expectedReplica.Key)
								for _, expectedChild := range expectedReplica.Components {
									child, err := replica.Component(expectedChild.Name)
									if err != nil {
										t.Fatal(err)
									}
									for _, expectedChildInstance := range expectedChild.Instances {
										bound, err := child.Instance(expectedChildInstance.Name)
										if err != nil {
											t.Fatal(err)
										}
										pods, err := bound.Pods()
										if err != nil {
											t.Fatal(err)
										}
										assertPods(t, pods, expectedChildInstance.PodUIDs)
										assertLiveMembershipCoordinate(t, memberships, expectedChildInstance.PodUIDs, expectedChild.Name, expectedChildInstance.Name, nil)
										for _, uid := range expectedChildInstance.PodUIDs {
											if memberships[types.UID(uid)].Component != expectedChild.Name {
												t.Fatalf("Pod %s selected component %s, want oracle component %s", uid, memberships[types.UID(uid)].Component, expectedChild.Name)
											}
										}
									}
								}
							}
						}
					}
					t.Logf("verified %s: %d exact Pod UIDs, source=%s", item.Root.Name, len(item.PodUIDs), workload.Observation().Source)
				})
			}
		})
	}
	if counted.ownerReads == 0 {
		t.Fatal("live fixtures did not exercise owner metadata reads")
	}
	if countedPodREST.ownerReads == 0 {
		t.Fatal("live fixtures did not exercise owner metadata reads through the Pod REST wrapper")
	}
	t.Logf("owner metadata reads: direct=%d, Pod REST=%d", counted.ownerReads, countedPodREST.ownerReads)
	if !t.Failed() {
		fmt.Printf("Independent oracle verified: %d workloads / %d Pods, direct, owner metadata, Pod REST, Pod REST with owner metadata and indexed\n", len(expected.Workloads), expected.PodCount)
	}
}
