// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package tree

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"
	jsonutil "k8s.io/apimachinery/pkg/util/json"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

// Editable owns a private workload and extracted tree. It never writes to Kubernetes.
type Editable interface {
	resource.Suspendable
	Snapshot() *WorkloadTree
	GetResource() (resource.KubernetesObject, error)
	Mutate(context.Context, ...Write) error
}

// Field identifies a built-in read/write accessor.
type Field string

const (
	PodTemplateSpec   Field = "podTemplateSpec"
	PodSpec           Field = "podSpec"
	Metadata          Field = "metadata"
	SchedulerName     Field = "fragmented.schedulerName"
	LabelsField       Field = "fragmented.labels"
	Annotations       Field = "fragmented.annotations"
	Resources         Field = "fragmented.resources"
	ResourceClaims    Field = "fragmented.resourceClaims"
	PodAffinity       Field = "fragmented.podAffinity"
	NodeAffinity      Field = "fragmented.nodeAffinity"
	Containers        Field = "fragmented.containers"
	Container         Field = "fragmented.container"
	PriorityClassName Field = "fragmented.priorityClassName"
	Image             Field = "fragmented.image"
	Replicas          Field = "scale.replicas"
	MinReplicas       Field = "scale.minReplicas"
	MaxReplicas       Field = "scale.maxReplicas"
)

var ErrNotSuspendable = errors.New("root component does not support suspension")

type Target struct {
	Component string
	Instance  string
	Field     Field
}

// Write applies a non-null JSON Merge Patch to a definition-owned destination.
// Maps retain unmentioned fields, null map members delete, and arrays replace.
type Write struct {
	Component string
	Instance  string
	Field     Field
	Value     any
}

type editableTree struct {
	mu       sync.RWMutex
	factory  *resource.ComponentFactory
	snapshot *WorkloadTree
}

func Open(ctx context.Context, definition *v1alpha1.Karta, workload resource.KubernetesObject, options ...resource.FactoryOption) (Editable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if definition == nil || workload == nil {
		return nil, errors.New("karta and workload are required")
	}
	factory := resource.NewComponentFactoryFromObject(definition, workload, options...)
	if _, err := factory.GetResource(); err != nil {
		return nil, err
	}
	snapshot, err := Build(ctx, factory)
	if err != nil {
		return nil, err
	}
	return &editableTree{factory: factory, snapshot: snapshot}, nil
}

func (t *editableTree) Snapshot() *WorkloadTree {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := &WorkloadTree{Children: cloneComponentNodes(t.snapshot.Children)}
	if t.snapshot.Status != nil {
		result.Status = &WorkloadStatus{Phases: append([]string{}, t.snapshot.Status.Phases...)}
	}
	return result
}

func (t *editableTree) GetResource() (resource.KubernetesObject, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.factory.GetResource()
}

func (t *editableTree) Mutate(ctx context.Context, writes ...Write) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	targets := make([]resource.WriteTarget, len(writes))
	values := make([]any, len(writes))
	for i, write := range writes {
		target, err := resolveTarget(ctx, t.factory, Target{Component: write.Component, Instance: write.Instance, Field: write.Field})
		if err != nil {
			return fmt.Errorf("write %d: %w", i, err)
		}
		targets[i] = target
		values[i] = write.Value
	}
	return t.publish(ctx, targets, values, false)
}

func (t *editableTree) publish(ctx context.Context, targets []resource.WriteTarget, values []any, assignment bool) error {
	if len(targets) == 0 {
		return nil
	}
	var staged *resource.ComponentFactory
	var err error
	if assignment {
		staged, err = t.factory.MutatedActions(ctx, targets, values)
	} else {
		staged, err = t.factory.Mutated(ctx, targets, values)
	}
	if err != nil {
		return err
	}
	snapshot, err := Build(ctx, staged)
	if err != nil {
		return fmt.Errorf("extract mutated workload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.factory, t.snapshot = staged, snapshot
	return nil
}

func (t *editableTree) IsSuspendable() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.factory.GetKarta().Spec.StructureDefinition.RootComponent.SuspendDefinition != nil
}

func (t *editableTree) Suspend(ctx context.Context) error { return t.setSuspended(ctx, true) }
func (t *editableTree) Resume(ctx context.Context) error  { return t.setSuspended(ctx, false) }

func (t *editableTree) setSuspended(ctx context.Context, suspend bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := t.factory.GetRootComponent()
	if err != nil {
		return err
	}
	def := root.Definition()
	if def.SuspendDefinition == nil {
		return ErrNotSuspendable
	}
	actions := def.SuspendDefinition.SuspendActions
	if !suspend {
		actions = def.SuspendDefinition.ResumeActions
	}
	ids, err := root.GetInstanceIds(ctx)
	if err != nil {
		return err
	}
	var targets []resource.WriteTarget
	var values []any
	for index, id := range ids {
		for _, action := range actions {
			target, err := t.factory.ResolveWriteTarget(ctx, &v1alpha1.ValueAccessor{Write: &action.Write}, id, index)
			if err != nil {
				return err
			}
			var value any
			if err := jsonutil.Unmarshal([]byte(action.Value), &value); err != nil {
				return err
			}
			targets = append(targets, target)
			values = append(values, value)
		}
	}
	return t.publish(ctx, targets, values, true)
}

func resolveTarget(ctx context.Context, factory *resource.ComponentFactory, target Target) (resource.WriteTarget, error) {
	component, err := factory.GetComponent(target.Component)
	if err != nil {
		return resource.WriteTarget{}, err
	}
	ids, err := component.GetInstanceIds(ctx)
	if err != nil {
		return resource.WriteTarget{}, err
	}
	index := -1
	for i, id := range ids {
		if id == target.Instance {
			index = i
			break
		}
	}
	if index < 0 {
		return resource.WriteTarget{}, fmt.Errorf("component %s has no instance %q", target.Component, target.Instance)
	}
	via := fieldAccessor(component.Definition(), target.Field)
	if via == nil {
		return resource.WriteTarget{}, fmt.Errorf("component %s has no field %q", target.Component, target.Field)
	}
	return factory.ResolveWriteTarget(ctx, via, target.Instance, index)
}

func fieldAccessor(def v1alpha1.ComponentDefinition, field Field) *v1alpha1.ValueAccessor {
	if scale := def.ScaleDefinition; scale != nil {
		switch field {
		case Replicas:
			return scale.Replicas
		case MinReplicas:
			return scale.MinReplicas
		case MaxReplicas:
			return scale.MaxReplicas
		}
	}
	spec := def.SpecDefinition
	if spec == nil {
		return nil
	}
	switch field {
	case PodTemplateSpec:
		return spec.PodTemplateSpec
	case PodSpec:
		return spec.PodSpec
	case Metadata:
		return spec.Metadata
	}
	f := spec.FragmentedPodSpecDefinition
	if f == nil {
		return nil
	}
	switch field {
	case SchedulerName:
		return f.SchedulerName
	case LabelsField:
		return f.Labels
	case Annotations:
		return f.Annotations
	case Resources:
		return f.Resources
	case ResourceClaims:
		return f.ResourceClaims
	case PodAffinity:
		return f.PodAffinity
	case NodeAffinity:
		return f.NodeAffinity
	case Containers:
		return f.Containers
	case Container:
		return f.Container
	case PriorityClassName:
		return f.PriorityClassName
	case Image:
		return f.Image
	default:
		return nil
	}
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneScale(scale *resource.Scale) *resource.Scale {
	if scale == nil {
		return nil
	}
	return &resource.Scale{Replicas: clonePointer(scale.Replicas), MinReplicas: clonePointer(scale.MinReplicas), MaxReplicas: clonePointer(scale.MaxReplicas)}
}

func cloneExtracted(value *resource.ExtractedInstance) *resource.ExtractedInstance {
	if value == nil {
		return nil
	}
	copy := *value
	copy.PodTemplateSpec = value.PodTemplateSpec.DeepCopy()
	copy.PodSpec = value.PodSpec.DeepCopy()
	copy.Metadata = value.Metadata.DeepCopy()
	copy.Scale = cloneScale(value.Scale)
	if f := value.FragmentedPodSpec; f != nil {
		fragment := *f
		fragment.Labels, fragment.Annotations = maps.Clone(f.Labels), maps.Clone(f.Annotations)
		fragment.Resources = f.Resources.DeepCopy()
		fragment.PodAffinity, fragment.NodeAffinity = f.PodAffinity.DeepCopy(), f.NodeAffinity.DeepCopy()
		fragment.Container = f.Container.DeepCopy()
		if f.Containers != nil {
			fragment.Containers = make([]corev1.Container, len(f.Containers))
			for i := range f.Containers {
				f.Containers[i].DeepCopyInto(&fragment.Containers[i])
			}
		}
		if f.ResourceClaims != nil {
			fragment.ResourceClaims = make([]corev1.PodResourceClaim, len(f.ResourceClaims))
			for i := range f.ResourceClaims {
				f.ResourceClaims[i].DeepCopyInto(&fragment.ResourceClaims[i])
			}
		}
		copy.FragmentedPodSpec = &fragment
	}
	return &copy
}
