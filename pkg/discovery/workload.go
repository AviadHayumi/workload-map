// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package discovery

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
	"github.com/dsx-ai-factory/workload-map/pkg/tree"
)

// Workload owns one detached observation of a root and its complete Pod set.
// Accessors never read the cluster. Refresh by acquiring another Workload.
type Workload struct {
	object      *unstructured.Unstructured
	definition  *v1alpha1.Karta
	prepared    *attribution.Prepared
	observation Observation
	pods        []*corev1.Pod
	memberships []attribution.Membership
	issues      []attribution.Issue
	issueRows   []int
	components  map[string]v1alpha1.ComponentDefinition
	paths       map[string][]string
	postings    map[postingKey][]int
}

type postingKey struct {
	component string
	axis      string
	value     string
}

// diagnostic is a value error so a caller cannot mutate a retained diagnostic.
type diagnostic string

func (d diagnostic) Error() string { return string(d) }

func buildWorkload(ctx context.Context, object *unstructured.Unstructured, pods []*corev1.Pod, plan *attribution.Plan, observation Observation) (*Workload, error) {
	w := &Workload{object: object, pods: pods, observation: observation,
		components: make(map[string]v1alpha1.ComponentDefinition), paths: make(map[string][]string), postings: make(map[postingKey][]int)}
	if plan == nil {
		// Generic ownership has no definition-derived semantics to guess.
		w.components["pods"] = v1alpha1.ComponentDefinition{Name: "pods"}
		w.paths["pods"] = []string{"pods"}
	} else {
		w.definition = plan.Definition()
		prepared, err := attribution.Prepare(ctx, plan, object)
		if err != nil {
			return nil, fmt.Errorf("prepare workload attribution: %w", err)
		}
		w.prepared = prepared
		all := append([]v1alpha1.ComponentDefinition{w.definition.Spec.StructureDefinition.RootComponent}, w.definition.Spec.StructureDefinition.ChildComponents...)
		for _, component := range all {
			w.components[component.Name] = component
		}
		for _, component := range all {
			var path []string
			for current := component.Name; current != ""; {
				path = append(path, current)
				parent := w.components[current].OwnerRef
				if parent == nil {
					break
				}
				current = *parent
			}
			slices.Reverse(path)
			w.paths[component.Name] = path
		}
	}
	for row, pod := range pods {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var membership attribution.Membership
		if plan == nil {
			anonymous := ""
			membership = attribution.Membership{Component: "pods", Path: []attribution.Coordinate{{Component: "pods", Instance: &anonymous}}}
		} else {
			var err error
			membership, err = attribution.Evaluate(ctx, pod, w.prepared)
			if err != nil {
				return nil, fmt.Errorf("attribute Pod %s: %w", pod.Name, err)
			}
		}
		for i := range membership.Issues {
			if membership.Issues[i].Cause != nil {
				membership.Issues[i].Cause = diagnostic(membership.Issues[i].Cause.Error())
			}
		}
		w.memberships = append(w.memberships, membership)
		w.issues = append(w.issues, membership.Issues...)
		if len(membership.Issues) > 0 {
			w.issueRows = append(w.issueRows, row)
		}
		for _, coordinate := range membership.Path {
			key := postingKey{component: coordinate.Component}
			w.postings[key] = append(w.postings[key], row)
			if coordinate.Instance != nil {
				key := postingKey{component: coordinate.Component, axis: "instance", value: *coordinate.Instance}
				w.postings[key] = append(w.postings[key], row)
			}
			if coordinate.Replica != nil {
				key := postingKey{component: coordinate.Component, axis: "replica", value: *coordinate.Replica}
				w.postings[key] = append(w.postings[key], row)
			}
		}
	}
	return w, nil
}

func (w *Workload) Root() ObjectRef                    { return reference(w.object) }
func (w *Workload) Object() *unstructured.Unstructured { return w.object.DeepCopy() }
func (w *Workload) Definition() *v1alpha1.Karta {
	if w.definition == nil {
		return nil
	}
	return w.definition.DeepCopy()
}
func (w *Workload) Generic() bool            { return w.definition == nil }
func (w *Workload) Observation() Observation { return w.observation }

// Pods returns all ownership-confirmed Pods even when semantic classification
// has issues. Every returned Pod is a caller-owned deep copy.
func (w *Workload) Pods() []*corev1.Pod {
	result := make([]*corev1.Pod, len(w.pods))
	for i, pod := range w.pods {
		result[i] = pod.DeepCopy()
	}
	return result
}

func (w *Workload) MembershipIssues() []attribution.Issue { return slices.Clone(w.issues) }

// Component returns a component scope including its descendant components.
// Generic workloads expose the single component "pods".
func (w *Workload) Component(name string) (*Component, error) {
	if _, ok := w.components[name]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrComponentNotFound, name)
	}
	return &Component{scope: scope{workload: w, component: name}}, nil
}

// DesiredTree explicitly evaluates the definition's spec, scale and status
// expressions on the captured object. It does not represent observed Pod
// membership. Discovery and scope queries never call this method.
func (w *Workload) DesiredTree(ctx context.Context) (*tree.WorkloadTree, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.Generic() {
		return nil, ErrUnsupported
	}
	return tree.Build(ctx, resource.NewComponentFactoryFromObject(w.Definition(), w.Object()))
}
