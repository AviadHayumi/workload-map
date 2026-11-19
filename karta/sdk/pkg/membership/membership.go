// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package membership attributes Pods to native workloads and explicit reference targets.
package membership

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	celpkg "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

// WorkloadInput pairs an original workload with its selected definition.
type WorkloadInput struct {
	Object     *unstructured.Unstructured
	Definition *api.Karta
}

// Input is one namespace observation. ScopeComplete certifies that every
// applicable workload and Pod in that namespace was successfully collected.
type Input struct {
	Workloads     []WorkloadInput
	Pods          []corev1.Pod
	ScopeComplete bool
}

// Snapshot supplies reference bindings and owner inventory without cluster reads.
// Complete certifies inventory completeness, not cluster freshness.
type Snapshot struct {
	References map[types.UID]references.ResolvedReferences
	Owners     []*unstructured.Unstructured
	Complete   bool
}

// ComponentKey identifies a component independently of its display name.
type ComponentKey struct {
	Workload  types.UID
	Component string
}

// Target is an accepted association. Foldable is true only when every observed
// descendant Pod is accounted for by this parent's accepted components.
type Target struct {
	Parent     types.UID
	Object     *unstructured.Unstructured
	Components []string
	Foldable   bool
}

// Result keeps native fallback and per-component reference membership separate.
// Callers fold Targets only when their Parent is displayed in an untyped view.
type Result struct {
	Pods        map[ComponentKey][]corev1.Pod
	NativePods  map[ComponentKey][]corev1.Pod
	Targets     map[types.UID]Target
	Complete    bool
	Diagnostics []string
}

type options struct {
	reader     references.ResourceReader
	snapshot   *Snapshot
	enabled    bool
	evaluation []celpkg.Option
}

// Option configures the source of this observation.
type Option func(*options)

func WithReader(reader references.ResourceReader) Option {
	return func(o *options) { o.reader = reader }
}
func WithSnapshot(snapshot Snapshot) Option     { return func(o *options) { o.snapshot = &snapshot } }
func WithReferencesEnabled(enabled bool) Option { return func(o *options) { o.enabled = enabled } }

// WithEvaluationLimits sets the CEL budget for reference recipes, targets and Pod selectors.
func WithEvaluationLimits(cost uint64, timeout time.Duration) Option {
	return func(o *options) { o.evaluation = []celpkg.Option{celpkg.WithLimits(cost, timeout)} }
}

type identity struct {
	gvk             schema.GroupVersionKind
	namespace, name string
}
type observation struct {
	reader  references.ResourceReader
	objects map[identity]*unstructured.Unstructured
	known   map[types.UID]*unstructured.Unstructured
}
type claim struct {
	key    ComponentKey
	target *unstructured.Unstructured
}
type podPath struct {
	ancestors map[types.UID]int
	err       error
}

// Resolve performs no writes. Failures in membership evidence discard additional
// membership for this namespace while retaining the native result. Invalid input
// and cancellation are errors. Use a separate call for each namespace.
func Resolve(ctx context.Context, input Input, opts ...Option) (*Result, error) {
	o := options{enabled: true}
	for _, opt := range opts {
		opt(&o)
	}
	o.reader = references.NewCachingReader(o.reader)
	if o.reader != nil && o.snapshot != nil {
		return nil, errors.New("membership reader and snapshot are mutually exclusive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &Result{Pods: map[ComponentKey][]corev1.Pod{}, NativePods: map[ComponentKey][]corev1.Pod{}, Targets: map[types.UID]Target{}, Complete: input.ScopeComplete}
	obs := observation{reader: o.reader, objects: map[identity]*unstructured.Unstructured{}, known: map[types.UID]*unstructured.Unstructured{}}
	namespace := ""
	roots := map[types.UID]WorkloadInput{}
	evaluation := map[types.UID][]celpkg.Option{}
	for _, w := range input.Workloads {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if w.Object == nil || w.Definition == nil || w.Object.GetUID() == "" {
			return nil, errors.New("membership requires workload objects, definitions and UIDs")
		}
		if err := api.NewKartaValidator(w.Definition).Validate(); err != nil {
			return nil, fmt.Errorf("invalid membership definition: %w", err)
		}
		programs, err := api.ProgramsFor(w.Definition)
		if err != nil {
			return nil, err
		}
		evaluation[w.Object.GetUID()] = append(o.evaluation[:len(o.evaluation):len(o.evaluation)], celpkg.WithPrograms(programs))
		if namespace == "" {
			namespace = w.Object.GetNamespace()
		}
		if namespace == "" || w.Object.GetNamespace() != namespace {
			return nil, errors.New("membership input must contain one nonempty namespace")
		}
		if _, ok := roots[w.Object.GetUID()]; ok {
			return nil, errors.New("duplicate workload UID")
		}
		roots[w.Object.GetUID()] = w
		obs.add(w.Object)
	}
	if len(input.Workloads) == 0 {
		return result, nil
	}
	if o.snapshot != nil {
		result.Complete = result.Complete && o.snapshot.Complete
		for _, obj := range o.snapshot.Owners {
			if obj != nil {
				obs.add(obj)
			}
		}
	}
	claims := []claim{}
	for _, w := range input.Workloads {
		if !o.enabled {
			break
		}
		cc, err := resolveClaims(ctx, w, o, &obs, evaluation[w.Object.GetUID()]...)
		if err != nil {
			result.Diagnostics = append(result.Diagnostics, "reference membership unavailable: "+err.Error())
			result.Complete = false
			continue
		}
		claims = append(claims, cc...)
	}
	paths := make(map[types.UID]podPath, len(input.Pods))
	seenPods := map[types.UID]bool{}
	for i := range input.Pods {
		p := &input.Pods[i]
		if p.Namespace != namespace || p.UID == "" || seenPods[p.UID] {
			return nil, errors.New("pods must have unique UIDs in the observation namespace")
		}
		seenPods[p.UID] = true
		path := obs.path(ctx, p)
		paths[p.UID] = path
		if path.err != nil {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, "owner evidence incomplete: "+path.err.Error())
		}
		for _, w := range input.Workloads {
			if _, owned := path.ancestors[w.Object.GetUID()]; !owned {
				continue
			}
			components := componentsOf(w.Definition)
			sole := podOwnerCount(components) == 1
			for _, c := range components {
				if c.PodSelector != nil && len(c.PodSelector.FromReferences) > 0 {
					continue
				}
				matches, err := matchesPod(ctx, c, p, sole, evaluation[w.Object.GetUID()]...)
				if err != nil {
					result.Complete = false
					result.Diagnostics = append(result.Diagnostics, "native component selector failed")
					continue
				}
				if matches {
					key := ComponentKey{w.Object.GetUID(), c.Name}
					result.NativePods[key] = append(result.NativePods[key], *p.DeepCopy())
				}
			}
		}
	}
	for k, pods := range result.NativePods {
		result.Pods[k] = slices.Clone(pods)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !o.enabled {
		return result, nil
	}
	if !result.Complete {
		return result, nil
	}
	rejected := map[types.UID]bool{}
	targetPaths := map[types.UID]podPath{}
	for _, c := range claims {
		uid := c.target.GetUID()
		path, exists := targetPaths[uid]
		if !exists {
			path = obs.path(ctx, c.target)
			targetPaths[uid] = path
		}
		if path.err != nil {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, "target owner evidence incomplete")
			continue
		}
		for ancestor := range path.ancestors {
			if _, native := roots[ancestor]; native && ancestor != uid && ancestor != c.key.Workload {
				rejected[uid] = true
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !result.Complete {
		return result, nil
	}

	// A referenced workload cannot itself act as an additional parent in this
	// observation. This prevents recursive folding and cycles.
	parents := map[types.UID]bool{}
	targetParents := map[types.UID]types.UID{}
	for _, c := range claims {
		parents[c.key.Workload] = true
	}
	for _, c := range claims {
		uid := c.target.GetUID()
		if uid == c.key.Workload || parents[uid] {
			rejected[uid] = true
		}
		if previous, ok := targetParents[uid]; ok && previous != c.key.Workload {
			rejected[uid] = true
		}
		targetParents[uid] = c.key.Workload
	}
	additional := map[ComponentKey]map[types.UID]corev1.Pod{}
	claimPods := map[types.UID][]claim{}
	descendants := map[types.UID]map[types.UID]bool{}
	for _, c := range claims {
		uid := c.target.GetUID()
		w := roots[c.key.Workload]
		var component api.ComponentDefinition
		for _, candidate := range componentsOf(w.Definition) {
			if candidate.Name == c.key.Component {
				component = candidate
				break
			}
		}
		for i := range input.Pods {
			p := &input.Pods[i]
			path := paths[p.UID]
			targetDepth, owned := path.ancestors[uid]
			if !owned {
				continue
			}
			if descendants[uid] == nil {
				descendants[uid] = map[types.UID]bool{}
			}
			descendants[uid][p.UID] = true
			matched, err := matchesPod(ctx, component, p, false, evaluation[c.key.Workload]...)
			if err != nil {
				result.Complete = false
				result.Diagnostics = append(result.Diagnostics, "reference component selector failed")
				continue
			}
			if !matched {
				continue
			}
			for native := range roots {
				if depth, exists := path.ancestors[native]; exists && native != c.key.Workload && native != uid && depth > targetDepth {
					rejected[uid] = true
				}
			}
			for nativeKey, pods := range result.NativePods {
				if !slices.ContainsFunc(pods, func(pod corev1.Pod) bool { return pod.UID == p.UID }) {
					continue
				}
				if nativeKey == c.key {
					continue
				}
				depth, exists := path.ancestors[nativeKey.Workload]
				if !exists || depth > targetDepth || nativeKey.Workload == c.key.Workload {
					rejected[uid] = true
				}
			}
			claimPods[p.UID] = append(claimPods[p.UID], c)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !result.Complete {
		return result, nil
	}
	for _, cc := range claimPods {
		for _, a := range cc {
			for _, b := range cc {
				if a.key != b.key {
					rejected[a.target.GetUID()] = true
					rejected[b.target.GetUID()] = true
				}
			}
		}
	}
	for uid, cc := range claimPods {
		for _, c := range cc {
			if rejected[c.target.GetUID()] {
				continue
			}
			if additional[c.key] == nil {
				additional[c.key] = map[types.UID]corev1.Pod{}
			}
			for i := range input.Pods {
				if input.Pods[i].UID == uid {
					additional[c.key][uid] = *input.Pods[i].DeepCopy()
					break
				}
			}
		}
	}
	// An explicit target set replaces root ownership for that component.
	for _, w := range input.Workloads {
		for _, c := range componentsOf(w.Definition) {
			if c.PodSelector != nil && len(c.PodSelector.FromReferences) > 0 {
				delete(result.Pods, ComponentKey{w.Object.GetUID(), c.Name})
			}
		}
	}
	for key, pods := range additional {
		for _, p := range input.Pods {
			if pod, ok := pods[p.UID]; ok {
				result.Pods[key] = append(result.Pods[key], pod)
			}
		}
	}
	for _, c := range claims {
		uid := c.target.GetUID()
		if rejected[uid] {
			continue
		}
		target := result.Targets[uid]
		target.Parent = c.key.Workload
		target.Object = c.target.DeepCopy()
		if !slices.Contains(target.Components, c.key.Component) {
			target.Components = append(target.Components, c.key.Component)
		}
		target.Foldable = true
		for podUID := range descendants[uid] {
			accounted := false
			for _, accepted := range claimPods[podUID] {
				if accepted.key.Workload == c.key.Workload && !rejected[accepted.target.GetUID()] {
					accounted = true
					break
				}
			}
			if !accounted {
				target.Foldable = false
				break
			}
		}
		result.Targets[uid] = target
	}
	uids := make([]string, 0, len(rejected))
	for uid := range rejected {
		uids = append(uids, string(uid))
	}
	slices.Sort(uids)
	for _, uid := range uids {
		claimants := []string{}
		for _, c := range claims {
			if string(c.target.GetUID()) != uid {
				continue
			}
			root := roots[c.key.Workload].Object
			name := fmt.Sprintf("%s/%s component %s (UID %s)", root.GetNamespace(), root.GetName(), c.key.Component, c.key.Workload)
			if !slices.Contains(claimants, name) {
				claimants = append(claimants, name)
			}
		}
		slices.Sort(claimants)
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("rejected membership for target UID %s; claimants: %s", uid, strings.Join(claimants, ", ")))
	}
	return result, nil
}

func componentsOf(def *api.Karta) []api.ComponentDefinition {
	return append([]api.ComponentDefinition{def.Spec.StructureDefinition.RootComponent}, def.Spec.StructureDefinition.ChildComponents...)
}
func podOwnerCount(components []api.ComponentDefinition) int {
	n := 0
	for _, c := range components {
		if c.SpecDefinition != nil && (c.PodSelector == nil || len(c.PodSelector.FromReferences) == 0) {
			n++
		}
	}
	return n
}
func matchesPod(ctx context.Context, c api.ComponentDefinition, p *corev1.Pod, sole bool, opts ...celpkg.Option) (bool, error) {
	if c.PodSelector == nil || c.PodSelector.ComponentTypeSelector == nil {
		return sole && c.SpecDefinition != nil, nil
	}
	return resource.NewPodQuerier(p, opts...).MatchesComponentType(ctx, c.PodSelector.ComponentTypeSelector)
}

func resolveClaims(ctx context.Context, w WorkloadInput, o options, obs *observation, opts ...celpkg.Option) ([]claim, error) {
	needed := map[string]bool{}
	for _, c := range componentsOf(w.Definition) {
		if c.PodSelector != nil {
			for _, r := range c.PodSelector.FromReferences {
				needed[r.Name] = true
			}
		}
	}
	if len(needed) == 0 {
		return nil, nil
	}
	declarations := []api.Reference{}
	plans := map[string]references.Query{}
	for _, r := range w.Definition.Spec.StructureDefinition.References {
		if needed[r.Name] {
			q, err := references.Plan(ctx, r, w.Object.Object, opts...)
			if err != nil {
				return nil, err
			}
			plans[r.Name] = q
			declarations = append(declarations, r)
		}
	}
	var values references.ResolvedReferences
	if o.snapshot != nil {
		all := o.snapshot.References[w.Object.GetUID()]
		values = references.ResolvedReferences{}
		for _, r := range declarations {
			if plans[r.Name].Active {
				if value, ok := all[r.Name]; ok {
					values[r.Name] = value
				} else if r.List != nil {
					return nil, errors.New("active membership list is missing from snapshot")
				}
			}
		}
		if err := references.ValidateSupplied(declarations, values); err != nil {
			return nil, err
		}
	} else {
		if o.reader == nil {
			for _, q := range plans {
				if q.Active {
					return nil, errors.New("active membership references require a reader or snapshot")
				}
			}
			return nil, nil
		}
		var err error
		values, err = references.ResolveDefinitions(ctx, o.reader, declarations, w.Object.Object, opts...)
		if err != nil {
			return nil, err
		}
	}
	bindings, err := values.Bindings()
	if err != nil {
		return nil, err
	}
	out := []claim{}
	for _, c := range componentsOf(w.Definition) {
		if c.PodSelector == nil {
			continue
		}
		for _, ref := range c.PodSelector.FromReferences {
			q := plans[ref.Name]
			if !q.Active {
				continue
			}
			raw, exists := bindings[ref.Name]
			if !exists {
				continue
			}
			objects := []any{raw}
			if list, ok := raw.([]any); ok {
				objects = list
			}
			for _, value := range objects {
				m, ok := value.(map[string]any)
				if !ok {
					return nil, errors.New("reference membership requires objects")
				}
				target := &unstructured.Unstructured{Object: m}
				if target.GroupVersionKind() != q.GVK || target.GetNamespace() != w.Object.GetNamespace() || target.GetNamespace() == "" || target.GetUID() == "" || target.GetName() == "" || target.GetKind() == "Pod" && target.GetAPIVersion() == "v1" {
					return nil, errors.New("invalid membership target identity or scope")
				}
				if q.Name != "" && target.GetName() != q.Name {
					return nil, errors.New("membership lookup name mismatch")
				}
				if q.Selector != nil && !q.Selector.Matches(labels.Set(target.GetLabels())) {
					return nil, errors.New("membership target does not match selector")
				}
				if ref.MatchExpression != "" {
					match, err := celpkg.EvaluateBool(ctx, celpkg.TargetScope, ref.MatchExpression, map[string]any{"object": w.Object.Object, "target": target.Object}, opts...)
					if err != nil {
						return nil, err
					}
					if !match {
						continue
					}
				}
				obs.add(target)
				out = append(out, claim{ComponentKey{w.Object.GetUID(), c.Name}, target.DeepCopy()})
			}
		}
	}
	return out, nil
}

func keyOf(obj *unstructured.Unstructured) identity {
	return identity{obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName()}
}
func (o *observation) add(obj *unstructured.Unstructured) {
	copy := obj.DeepCopy()
	o.objects[keyOf(copy)] = copy
	o.known[copy.GetUID()] = copy
}
func (o *observation) owner(ctx context.Context, namespace string, ref metav1.OwnerReference) (*unstructured.Unstructured, error) {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return nil, errors.New("invalid owner apiVersion")
	}
	key := identity{gv.WithKind(ref.Kind), namespace, ref.Name}
	obj := o.objects[key]
	if obj == nil {
		if o.reader == nil {
			return nil, errors.New("owner is missing from snapshot")
		}
		obj, err = o.reader.Get(ctx, key.gvk, namespace, ref.Name)
		if err != nil {
			return nil, errors.New("owner read failed")
		}
		if obj == nil {
			return nil, errors.New("owner is missing")
		}
		if obj.GroupVersionKind() != key.gvk || obj.GetNamespace() != namespace || obj.GetName() != ref.Name {
			return nil, errors.New("owner identity mismatch")
		}
		o.add(obj)
	}
	if ref.UID == "" || obj.GetUID() != ref.UID {
		return nil, errors.New("owner UID mismatch")
	}
	return obj, nil
}
func (o *observation) path(ctx context.Context, p metav1.Object) podPath {
	result := podPath{ancestors: map[types.UID]int{p.GetUID(): 0}}
	refs := p.GetOwnerReferences()
	visited := map[types.UID]bool{p.GetUID(): true}
	for depth := 1; depth <= 6; depth++ {
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		var controller *unstructured.Unstructured
		for _, ref := range refs {
			isController := ref.Controller != nil && *ref.Controller
			if !isController && o.known[ref.UID] == nil {
				continue
			}
			owner, err := o.owner(ctx, p.GetNamespace(), ref)
			if err != nil {
				result.err = err
				return result
			}
			if visited[owner.GetUID()] {
				result.err = errors.New("owner cycle")
				return result
			}
			result.ancestors[owner.GetUID()] = depth
			if isController {
				if controller != nil {
					result.err = errors.New("multiple controller owners")
					return result
				}
				controller = owner
			}
		}
		if controller == nil {
			return result
		}
		visited[controller.GetUID()] = true
		refs = controller.GetOwnerReferences()
		if len(refs) == 0 {
			return result
		}
	}
	result.err = errors.New("owner depth limit exceeded")
	return result
}
