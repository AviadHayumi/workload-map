// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	"github.com/dsx-ai-factory/workload-map/exporter/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/kartadefinitioninventory"
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/store"
)

func TestRuntimeValidatesResourceRules(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rule      ResourceRule
		wantValid bool
	}{
		{"core", ResourceRule{[]string{""}, []string{"v1"}, []string{"pods"}}, true},
		{"named group", ResourceRule{[]string{"example.com"}, []string{"v1beta1"}, []string{"trainings"}}, true},
		{"wildcards", ResourceRule{[]string{"*"}, []string{"*"}, []string{"*"}}, true},
		{"alternatives", ResourceRule{[]string{"", "batch"}, []string{"v1", "v2"}, []string{"pods", "jobs"}}, true},
		{"missing groups", ResourceRule{nil, []string{"v1"}, []string{"pods"}}, false},
		{"missing versions", ResourceRule{[]string{""}, nil, []string{"pods"}}, false},
		{"missing resources", ResourceRule{[]string{""}, []string{"v1"}, nil}, false},
		{"empty version", ResourceRule{[]string{"batch"}, []string{"v1", ""}, []string{"jobs"}}, false},
		{"empty resource", ResourceRule{[]string{"batch"}, []string{"v1"}, []string{"jobs", ""}}, false},
		{"group wildcard", ResourceRule{[]string{"*.example.com"}, []string{"v1"}, []string{"jobs"}}, false},
		{"version wildcard", ResourceRule{[]string{"batch"}, []string{"v*"}, []string{"jobs"}}, false},
		{"resource wildcard", ResourceRule{[]string{"batch"}, []string{"v1"}, []string{"job*"}}, false},
		{"double wildcard", ResourceRule{[]string{"batch"}, []string{"v1"}, []string{"**"}}, false},
		{"subresource", ResourceRule{[]string{"batch"}, []string{"v1"}, []string{"jobs/status"}}, false},
		{"wildcard subresource", ResourceRule{[]string{"batch"}, []string{"v1"}, []string{"jobs/*"}}, false},
		{"all subresources", ResourceRule{[]string{"*"}, []string{"*"}, []string{"*/*"}}, false},
		{"whitespace", ResourceRule{[]string{"batch"}, []string{" "}, []string{"jobs"}}, false},
	} {
		for _, excluded := range []bool{false, true} {
			name := "resourceRules/"
			if excluded {
				name = "excludeResourceRules/"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				client := fake.NewSimpleDynamicClient(runtime.NewScheme())
				inventory, err := kartadefinitioninventory.New(client, kartadefinitioninventory.Options{})
				if err != nil {
					t.Fatal(err)
				}
				options := RuntimeOptions{}
				if excluded {
					options.ExcludeResourceRules = []ResourceRule{tc.rule}
				} else {
					options.ResourceRules = []ResourceRule{tc.rule}
				}
				reader := NewInformerReader()
				controller, err := NewRuntime(client, nil, meta.NewDefaultRESTMapper(nil), reader,
					discovery.New(reader, inventory), inventory, nil, store.New(), options)
				if controller != nil {
					controller.queue.ShutDown()
				}
				if (err == nil) != tc.wantValid {
					t.Fatalf("valid = %v, want %v; err = %v", err == nil, tc.wantValid, err)
				}
			})
		}
	}
	if err := ValidateResourceRules(nil); err != nil {
		t.Fatalf("unrestricted empty rule set rejected: %v", err)
	}
}
