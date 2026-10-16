// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
)

func TestPodMembershipsCopyIsolation(t *testing.T) {
	for _, mode := range []string{"direct", "indexed", "cached"} {
		t.Run(mode, func(t *testing.T) {
			f := attributionFixtureFor(t, 2)
			var reader Reader = f.index
			options := Options{Definitions: []*v1alpha1.Karta{f.definition}}
			switch mode {
			case "direct":
				reader = struct{ Reader }{reader}
			case "cached":
				options.Cache = &CacheOptions{MaxWorkloads: 4, MaxPods: 8}
			}
			var err error
			f.resolver, err = New(reader, options)
			if err != nil {
				t.Fatal(err)
			}
			first := attributionQuery(t, f)
			second := attributionQuery(t, f)
			if mode == "cached" && &first.memberships[0] != &second.memberships[0] {
				t.Fatal("cached observations did not share captured attribution")
			}
			want := make(map[types.UID]attribution.Membership)
			for _, pod := range f.pods {
				want[pod.UID] = attribution.Membership{Component: "worker", Path: []attribution.Coordinate{
					{Component: "root", Instance: ptr.To("")},
					{Component: "worker", Instance: ptr.To(""), Replica: ptr.To("a")},
				}}
			}
			got := first.PodMemberships()
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("PodMemberships() mismatch (-want +got):\n%s", diff)
			}
			for uid, membership := range got {
				membership.Component = "changed"
				for i := range membership.Path {
					coordinate := &membership.Path[i]
					coordinate.Component = "changed"
					if coordinate.Instance != nil {
						*coordinate.Instance = "changed"
					}
					if coordinate.Replica != nil {
						*coordinate.Replica = "changed"
					}
				}
				got[uid] = membership
			}
			delete(got, f.pods[0].UID)
			got["unrelated"] = attribution.Membership{Component: "extra"}
			for _, observation := range []*Workload{first, second, attributionQuery(t, f)} {
				if diff := cmp.Diff(want, observation.PodMemberships()); diff != "" {
					t.Fatalf("caller changed captured membership (-want +got):\n%s", diff)
				}
			}

			updated := f.pods[0].DeepCopy()
			updated.ResourceVersion = "2"
			updated.Labels["replica"] = "b"
			object, err := podObject(updated)
			if err != nil {
				t.Fatal(err)
			}
			attributionApply(t, f, Changes{Upserts: []*unstructured.Unstructured{object}})
			current := attributionQuery(t, f)
			for _, observation := range []*Workload{first, second} {
				if diff := cmp.Diff(want, observation.PodMemberships()); diff != "" {
					t.Fatalf("index update changed earlier membership (-want +got):\n%s", diff)
				}
			}
			want[updated.UID].Path[1].Replica = ptr.To("b")
			if diff := cmp.Diff(want, current.PodMemberships()); diff != "" {
				t.Fatalf("updated PodMemberships() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodMembershipsIncludeIncompleteResults(t *testing.T) {
	f := attributionFixtureFor(t, 3)
	f.pods[0].Labels["role"] = "unmatched"
	delete(f.pods[1].Labels, "replica")
	if _, err := f.index.Replace(t.Context(), f.dataset); err != nil {
		t.Fatal(err)
	}
	observed := attributionQuery(t, f)
	want := map[types.UID]attribution.Membership{
		f.pods[0].UID: {Issues: []attribution.Issue{{PodUID: f.pods[0].UID, Axis: "component"}}},
		f.pods[1].UID: {Component: "worker", Path: []attribution.Coordinate{
			{Component: "root", Instance: ptr.To("")},
			{Component: "worker", Instance: ptr.To("")},
		}, Issues: []attribution.Issue{{PodUID: f.pods[1].UID, Component: "worker", Axis: "replica"}}},
		f.pods[2].UID: {Component: "worker", Path: []attribution.Coordinate{
			{Component: "root", Instance: ptr.To("")},
			{Component: "worker", Instance: ptr.To(""), Replica: ptr.To("a")},
		}},
	}
	got := observed.PodMemberships()
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(attribution.Issue{}, "Cause")); diff != "" {
		t.Fatalf("incomplete PodMemberships() mismatch (-want +got):\n%s", diff)
	}
	for _, membership := range got {
		for i := range membership.Issues {
			if _, ok := membership.Issues[i].Cause.(diagnostic); !ok {
				t.Fatalf("membership cause = %T, want immutable diagnostic", membership.Issues[i].Cause)
			}
			membership.Issues[i] = attribution.Issue{PodUID: "changed", Component: "changed", Axis: "changed"}
		}
	}
	for _, observation := range []*Workload{observed, attributionQuery(t, f)} {
		fresh := observation.PodMemberships()
		if diff := cmp.Diff(want, fresh, cmpopts.IgnoreFields(attribution.Issue{}, "Cause")); diff != "" {
			t.Fatalf("caller changed incomplete membership (-want +got):\n%s", diff)
		}
		for _, membership := range fresh {
			for _, issue := range membership.Issues {
				if issue.Cause == nil {
					t.Fatal("caller removed a captured issue cause")
				}
			}
		}
	}
}

func TestPodMembershipsGenericAndEmpty(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			f := attributionFixtureFor(t, count)
			var err error
			f.resolver, err = New(f.index, Options{Cache: &CacheOptions{MaxWorkloads: 4, MaxPods: 8}})
			if err != nil {
				t.Fatal(err)
			}
			want := make(map[types.UID]attribution.Membership)
			for _, pod := range f.pods {
				want[pod.UID] = attribution.Membership{Component: "pods", Path: []attribution.Coordinate{
					{Component: "pods", Instance: ptr.To("")},
				}}
			}
			observed := attributionQuery(t, f)
			got := observed.PodMemberships()
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("generic PodMemberships() mismatch (-want +got):\n%s", diff)
			}
			for _, membership := range got {
				*membership.Path[0].Instance = "changed"
			}
			got["extra"] = attribution.Membership{Component: "changed"}
			for _, observation := range []*Workload{observed, attributionQuery(t, f)} {
				if diff := cmp.Diff(want, observation.PodMemberships()); diff != "" {
					t.Fatalf("caller changed generic membership (-want +got):\n%s", diff)
				}
			}
		})
	}
}
