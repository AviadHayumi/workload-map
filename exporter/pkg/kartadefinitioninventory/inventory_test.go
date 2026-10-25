// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

var jobGVK = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}

func job(t *testing.T, name string, seconds int64) *v1alpha1.Karta {
	t.Helper()
	d, err := catalog.Get(jobGVK)
	if err != nil {
		t.Fatal(err)
	}
	d.Name = name
	d.UID = types.UID(name)
	d.ResourceVersion = "1"
	d.CreationTimestamp = metav1.NewTime(time.Unix(seconds, 0))
	return d
}
func object(t *testing.T, d *v1alpha1.Karta) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(d)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Karta"))
	return u
}
func client() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{v1alpha1.GroupVersion.WithResource("kartas"): "KartaList"})
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func TestFilters(t *testing.T) {
	d := job(t, "named", 1)
	d.Labels = map[string]string{"team": "a"}
	for _, tc := range []struct {
		name string
		opts SourceOptions
		want bool
	}{
		{"disabled", SourceOptions{}, false},
		{"all", SourceOptions{Enabled: true}, true},
		{"name", SourceOptions{Enabled: true, Include: []string{"named"}}, true},
		{"label OR name", SourceOptions{Enabled: true, Include: []string{"other"}, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}}, true},
		{"name OR label", SourceOptions{Enabled: true, Include: []string{"named"}, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "b"}}}, true},
		{"exclude wins", SourceOptions{Enabled: true, Include: []string{"named"}, Exclude: []string{"named"}}, false},
		{"labels AND", SourceOptions{Enabled: true, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a", "other": "b"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := newFilter(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.matches(d); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if _, err := newFilter(SourceOptions{LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "x", Operator: "invalid"}}}}); err == nil {
		t.Fatal("invalid selector accepted")
	}
}
func TestCatalogAndConfiguration(t *testing.T) {
	options := DefaultOptions()
	options.Catalog.Enabled = true
	options.Cluster.Enabled = false
	i, err := New(nil, options)
	if err != nil {
		t.Fatal(err)
	}
	s, err := i.Lookup(jobGVK)
	if err != nil {
		t.Fatal(err)
	}
	if s.Definition == nil || s.Origin.Source != SourceCatalog || s.Origin.UID != "" || s.Origin.ResourceVersion != "" {
		t.Fatalf("bad catalog selection: %+v", s)
	}
	s.Definition.Name = "mutated"
	s, err = i.Lookup(jobGVK)
	if err != nil || s.Definition.Name == "mutated" {
		t.Fatal("snapshot mutation leaked")
	}
	pod := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	if s, err := i.Lookup(pod); err != nil || s.Definition != nil {
		t.Fatal("Pod selected by default")
	}
	options.Catalog.Exclude = nil
	i, err = New(nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := i.Lookup(pod); err != nil || s.Definition == nil {
		t.Fatalf("Pod opt in failed: %v", err)
	}
	options.Catalog.Include = []string{"unknown"}
	if _, err := New(nil, options); err == nil {
		t.Fatal("unknown name accepted")
	}
	if _, err := New(nil, DefaultOptions()); err == nil {
		t.Fatal("nil cluster client accepted")
	}
}
func TestPrecedenceAndInvalidWinner(t *testing.T) {
	i, err := New(client(), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Lookup(jobGVK); !errors.Is(err, ErrNotReady) {
		t.Fatalf("uninitialized source hidden: %v", err)
	}
	a, b, c := job(t, "a", 2), job(t, "b", 1), job(t, "c", 1)
	i.cluster = map[string]*v1alpha1.Karta{"a": a, "b": b, "c": c}
	i.rebuild()
	i.sourceErr = nil
	query := jobGVK
	query.Version = "v999"
	s, err := i.Lookup(query)
	if err != nil {
		t.Fatal(err)
	}
	if s.Origin.Name != "b" || s.GVK != jobGVK || s.Origin.UID != "b" || s.Origin.ResourceVersion != "1" {
		t.Fatalf("incorrect winner: %+v", s)
	}
	b.Spec.StructureDefinition.RootComponent.Name = ""
	i.rebuild()
	if _, err := i.Lookup(jobGVK); err == nil {
		t.Fatal("invalid winner fell back")
	}
	i.sourceErr = errors.New("forbidden")
	if _, err := i.Lookup(jobGVK); err == nil {
		t.Fatal("failed source hidden")
	}
}
func TestLifecycleAndConcurrentSnapshots(t *testing.T) {
	c := client()
	stream := watch.NewRaceFreeFake()
	c.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) { return true, stream, nil })
	i, err := New(c, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- i.Run(ctx) }()
	eventually(t, func() bool { return i.Ready() == nil })
	d := job(t, "cluster", 1)
	stream.Add(object(t, d))
	eventually(t, func() bool { s, e := i.Lookup(jobGVK); return e == nil && s.Origin.Source == SourceCluster })
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				s, e := i.Lookup(jobGVK)
				if e == nil && s.Definition != nil {
					s.Definition.Labels = map[string]string{"caller": "mutated"}
				}
			}
		}()
	}
	for n := 2; n < 15; n++ {
		d.ResourceVersion = fmt.Sprint(n)
		stream.Modify(object(t, d))
	}
	wg.Wait()
	eventually(t, func() bool { s, e := i.Lookup(jobGVK); return e == nil && s.Origin.ResourceVersion == "14" })
	stale := d.DeepCopy()
	stale.UID = "old"
	stream.Delete(object(t, stale))
	d.ResourceVersion = "15"
	stream.Modify(object(t, d))
	eventually(t, func() bool { s, e := i.Lookup(jobGVK); return e == nil && s.Origin.ResourceVersion == "15" })
	stream.Delete(object(t, d))
	eventually(t, func() bool { s, e := i.Lookup(jobGVK); return e == nil && s.Origin.Source == SourceCatalog })
	if err := i.Run(ctx); err == nil {
		t.Fatal("second Run accepted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := i.Lookup(jobGVK); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrPending) {
		t.Fatal("stopped source remained ready")
	}
}
func TestListWatchFailureAndRecovery(t *testing.T) {
	c := client()
	var mu sync.Mutex
	fail := true
	stream := watch.NewRaceFreeFake()
	c.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return true, nil, errors.New("forbidden")
		}
		return true, &unstructured.UnstructuredList{Object: map[string]interface{}{"metadata": map[string]interface{}{"resourceVersion": "5"}}}, nil
	})
	c.PrependWatchReactor("kartas", func(a ktesting.Action) (bool, watch.Interface, error) {
		if got := a.(ktesting.WatchAction).GetWatchRestrictions().ResourceVersion; got != "5" {
			t.Errorf("watch resourceVersion=%q", got)
		}
		return true, stream, nil
	})
	i, err := New(c, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- i.Run(ctx) }()
	eventually(t, func() bool { e := i.Ready(); return errors.Is(e, ErrUnavailable) })
	if _, err := i.Lookup(jobGVK); err == nil {
		t.Fatal("LIST failure masked by catalog")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	eventually(t, func() bool { return i.Ready() == nil })
	stream.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
	eventually(t, func() bool { return i.Ready() != nil })
	cancel()
	<-done
}

func testOptions() Options { o := DefaultOptions(); o.Catalog.Enabled = true; return o }

func TestPaginatedListAndWatchStartFailure(t *testing.T) {
	c := client()
	calls := 0
	c.PrependReactor("list", "kartas", func(_ ktesting.Action) (bool, runtime.Object, error) {
		calls++
		list := &unstructured.UnstructuredList{}
		list.SetResourceVersion("10")
		switch calls {
		case 1:
			list.SetContinue("next")
			list.Items = []unstructured.Unstructured{*object(t, job(t, "later", 2))}
		case 2:
			list.Items = []unstructured.Unstructured{*object(t, job(t, "oldest", 1))}
		default:
			t.Fatalf("unexpected list call %d", calls)
		}
		return true, list, nil
	})
	c.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) { return true, nil, errors.New("watch forbidden") })
	i, err := New(c, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := i.synchronize(context.Background()); err == nil {
		t.Fatal("watch failure ignored")
	}
	if calls != 2 {
		t.Fatalf("LIST pages: %d", calls)
	}
	if _, err := i.Lookup(jobGVK); err == nil {
		t.Fatal("published before watch established")
	}
}
func TestDisabledSourcesAndOptionSnapshot(t *testing.T) {
	i, err := New(nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s, err := i.Lookup(jobGVK); err != nil || s.Definition != nil {
		t.Fatal("disabled source contributed")
	}
	o := DefaultOptions()
	o.Cluster.Enabled = false
	o.Catalog.Enabled = true
	o.Catalog.Include = []string{"batch-job-v1"}
	i, err = New(nil, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Catalog.Include[0] = "core-pod-v1"
	if s, err := i.Lookup(jobGVK); err != nil || s.Definition == nil {
		t.Fatal("caller changed options after construction")
	}
}

func TestHTTPListPaginationResourceVersion(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("watch") == "true" {
			if r.URL.Query().Get("resourceVersion") != "30" {
				t.Error("watch did not resume LIST resource version")
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion("run.ai/v1alpha1")
		list.SetKind("KartaList")
		list.SetResourceVersion("30")
		if calls == 1 {
			if r.URL.Query().Get("limit") != "500" {
				t.Error("missing pagination limit")
			}
			list.SetContinue("page-two")
		} else if r.URL.Query().Get("continue") != "page-two" {
			t.Error("missing continuation token")
		}
		list.Items = []unstructured.Unstructured{*object(t, job(t, fmt.Sprintf("page-%d", calls), int64(calls)))}
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	var listContexts []context.Context
	watched := false
	c, err := dynamic.NewForConfig(&rest.Config{
		Host: server.URL,
		WrapTransport: func(next http.RoundTripper) http.RoundTripper {
			return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				deadline, bounded := request.Context().Deadline()
				if request.URL.Query().Get("watch") == "true" {
					watched = true
					if bounded {
						t.Error("WATCH inherited a LIST deadline")
					}
				} else {
					listContexts = append(listContexts, request.Context())
					if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second {
						t.Errorf("LIST deadline = %v, bounded = %t; want at most 30 seconds", deadline, bounded)
					}
				}
				return next.RoundTrip(request)
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	i, err := New(c, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := i.synchronize(context.Background()); err == nil {
		t.Fatal("watch authorization failure ignored")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("pages=%d", calls)
	}
	if len(listContexts) != 2 || !watched {
		t.Fatalf("LIST requests = %d, watched = %t; want two pages and WATCH", len(listContexts), watched)
	}
	for _, ctx := range listContexts {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("completed LIST page context error = %v, want context.Canceled", ctx.Err())
		}
	}
	if _, err := i.Lookup(jobGVK); err == nil {
		t.Fatal("partial source published")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSelectionHelpersAndSourceErrors(t *testing.T) {
	valid := job(t, "valid", 2)
	invalid := job(t, "invalid", 1)
	invalid.Spec.StructureDefinition.RootComponent.Name = ""
	o := testOptions()
	snapshot, err := NewSnapshot(o, []*v1alpha1.Karta{valid})
	if err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.Lookup(jobGVK)
	if err != nil || s.Origin.Name != "valid" {
		t.Fatalf("helper selection: %+v %v", s, err)
	}
	snapshot, err = NewSnapshot(o, []*v1alpha1.Karta{valid, invalid})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Lookup(jobGVK); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("invalid winner: %v", err)
	}
	i, err := newInventory(o, catalog.List(), []*v1alpha1.Karta{invalid})
	if err != nil {
		t.Fatal(err)
	}
	all, err := i.Selections()
	if !errors.Is(err, ErrInvalidDefinition) || len(all) == 0 {
		t.Fatalf("partial selections: %d %v", len(all), err)
	}
	found := false
	for _, s := range all {
		if s.GVK.GroupKind() == jobGVK.GroupKind() {
			found = s.Origin.Name == "invalid" && s.Definition != nil
		}
	}
	if !found {
		t.Fatal("invalid winner missing from selections")
	}
	all[0].Definition.Name = "changed"
	again, _ := i.Selections()
	if again[0].Definition.Name == "changed" {
		t.Fatal("Selections aliases cache")
	}
	i.fail(errors.New("connection reset"))
	if errors.Is(i.Ready(), ErrPending) || !errors.Is(i.Ready(), ErrUnavailable) {
		t.Fatal("missing source error sentinels")
	}
	if all, err := i.Selections(); err == nil || all != nil {
		t.Fatal("unavailable source emitted selections")
	}
	i.fail(apierrors.NewForbidden(schema.GroupResource{Group: "run.ai", Resource: "kartas"}, "", errors.New("denied")))
	if !errors.Is(i.Ready(), ErrPermissionDenied) || errors.Is(i.Ready(), ErrPending) {
		t.Fatal("missing permission sentinels")
	}
}

func TestIncompleteListsNeverPublish(t *testing.T) {
	for _, mode := range []string{"repeated token", "late page failure", "changed resource version"} {
		t.Run(mode, func(t *testing.T) {
			c := client()
			calls := 0
			c.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
				calls++
				if calls > 3 {
					t.Fatal("unbounded pagination")
				}
				if calls == 2 && mode == "late page failure" {
					return true, nil, errors.New("late page failed")
				}
				list := &unstructured.UnstructuredList{}
				list.SetResourceVersion("1")
				list.SetContinue("same")
				if calls == 2 && mode == "changed resource version" {
					list.SetResourceVersion("2")
				}
				list.Items = []unstructured.Unstructured{*object(t, job(t, "cluster", 1))}
				return true, list, nil
			})
			i, err := New(c, testOptions())
			if err != nil {
				t.Fatal(err)
			}
			if err := i.synchronize(context.Background()); err == nil {
				t.Fatal("incomplete list accepted")
			}
			if calls != 2 {
				t.Fatalf("LIST calls=%d", calls)
			}
			if _, err := i.Lookup(jobGVK); !errors.Is(err, ErrPending) {
				t.Fatal("partial list published")
			}
		})
	}
}

func TestSnapshotOptionalStatusAndInvalidSelections(t *testing.T) {
	d := job(t, "optional-status", 1)
	d.Spec.StructureDefinition.RootComponent.StatusDefinition = nil
	snapshot, err := NewSnapshot(testOptions(), []*v1alpha1.Karta{d})
	if err != nil {
		t.Fatal(err)
	}
	d.Name = "caller-mutated"
	selected, err := snapshot.Lookup(jobGVK)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Origin.Name != "optional-status" || selected.Definition.Spec.StructureDefinition.RootComponent.StatusDefinition != nil {
		t.Fatal("snapshot or optional status mutated")
	}
	all, err := snapshot.Selections()
	if err != nil || len(all) == 0 {
		t.Fatalf("optional status rejected: %v", err)
	}
	invalid := job(t, "invalid", 1)
	invalid.Spec.StructureDefinition.RootComponent.Name = ""
	snapshot, err = NewSnapshot(testOptions(), []*v1alpha1.Karta{invalid})
	if err != nil {
		t.Fatal(err)
	}
	all, err = snapshot.Selections()
	if !errors.Is(err, ErrInvalidDefinition) {
		t.Fatalf("invalid error: %v", err)
	}
	found := false
	for _, s := range all {
		if s.GVK == jobGVK {
			found = s.Origin.Name == invalid.Name && s.Origin.UID == invalid.UID && s.Origin.ResourceVersion == invalid.ResourceVersion && s.Definition != nil
		}
	}
	if !found {
		t.Fatal("invalid winner provenance lost")
	}
	if selected, err := snapshot.Lookup(jobGVK); !errors.Is(err, ErrInvalidDefinition) || selected.Definition != nil {
		t.Fatal("invalid strict lookup fell back")
	}
}

func TestExcludedMissingRootKindMatchesLiveSource(t *testing.T) {
	malformed := job(t, "excluded", 1)
	malformed.Spec.StructureDefinition.RootComponent.Kind = nil
	valid := job(t, "selected", 2)
	o := testOptions()
	o.Cluster.Exclude = []string{"excluded"}
	snapshot, err := NewSnapshot(o, []*v1alpha1.Karta{malformed, valid})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := snapshot.Lookup(jobGVK)
	if err != nil {
		t.Fatal(err)
	}
	c := client()
	stream := watch.NewRaceFreeFake()
	c.PrependReactor("list", "kartas", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*object(t, malformed), *object(t, valid)}}, nil
	})
	c.PrependWatchReactor("kartas", func(ktesting.Action) (bool, watch.Interface, error) { return true, stream, nil })
	i, err := New(c, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- i.Run(ctx) }()
	eventually(t, func() bool { return i.Ready() == nil })
	got, err := i.Lookup(jobGVK)
	if err != nil || got.Origin != expected.Origin {
		t.Fatalf("live/helper mismatch: %+v %v", got, err)
	}
	malformed.ResourceVersion = "2"
	stream.Modify(object(t, malformed))
	valid.ResourceVersion = "2"
	stream.Modify(object(t, valid))
	eventually(t, func() bool { s, e := i.Lookup(jobGVK); return e == nil && s.Origin.ResourceVersion == "2" })
	if _, err := i.Selections(); err != nil {
		t.Fatalf("excluded malformed definition poisoned source: %v", err)
	}
	cancel()
	<-done
}

func TestCandidateSkipsValidationAndCachesConcurrentValidation(t *testing.T) {
	invalid := job(t, "invalid", 1)
	invalid.Spec.StructureDefinition.RootComponent.Name = ""
	snapshot, err := NewSnapshot(testOptions(), []*v1alpha1.Karta{invalid})
	if err != nil {
		t.Fatal(err)
	}
	elected, ok := snapshot.Candidate(jobGVK)
	if !ok || elected.Origin.Name != "invalid" || elected.Definition == nil {
		t.Fatal("candidate lost invalid winner")
	}
	elected.Definition.Name = "mutated"
	if again, _ := snapshot.Candidate(jobGVK); again.Definition.Name != "invalid" {
		t.Fatal("candidate aliases snapshot")
	}
	if _, ok := snapshot.Candidate(schema.GroupVersionKind{Kind: "Missing"}); ok {
		t.Fatal("absent candidate exists")
	}
	var calls atomic.Int32
	selectedCandidate := makeCandidateWithValidator(invalid, SourceCluster, func(d *v1alpha1.Karta) error {
		calls.Add(1)
		if d == invalid {
			t.Error("validator received caller-owned definition")
		}
		return errors.New("invalid test definition")
	})
	if calls.Load() != 0 {
		t.Fatal("eager candidate validation")
	}
	snapshot.inventory.selected = map[schema.GroupKind]candidate{jobGVK.GroupKind(): selectedCandidate}
	if _, ok := snapshot.Candidate(jobGVK); !ok || calls.Load() != 0 {
		t.Fatal("Candidate performed validation")
	}
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := snapshot.Lookup(jobGVK); !errors.Is(err, ErrInvalidDefinition) {
				t.Errorf("Lookup error: %v", err)
			}
			if _, err := snapshot.Selections(); !errors.Is(err, ErrInvalidDefinition) {
				t.Errorf("Selections error: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("validation calls=%d", calls.Load())
	}
}
