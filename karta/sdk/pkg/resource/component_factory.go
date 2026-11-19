// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	celpkg "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

type ComponentReader interface {
	ExtractPodTemplateSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodTemplateSpec, error)
	ExtractPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodSpec, error)
	ExtractPodMetadata(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]metav1.ObjectMeta, error)
	ExtractFragmentedPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]FragmentedPodSpec, error)
	ExtractScale(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]Scale, error)
	ExtractStatus(ctx context.Context, definition v1alpha1.ComponentDefinition) (*Status, error)
	ExtractInstanceIds(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]string, error)
	GetObject() (map[string]interface{}, error)
}

type ComponentWriter interface {
	ApplySuspendActions(context.Context, v1alpha1.ComponentDefinition) error
	ApplyResumeActions(context.Context, v1alpha1.ComponentDefinition) error
}

//go:generate go run go.uber.org/mock/mockgen -source=component_factory.go -destination=accessor_mock.go -package=resource ComponentAccessor
type ComponentAccessor interface {
	ComponentReader
	ComponentWriter
}

type ComponentFactory struct {
	karta    *v1alpha1.Karta
	accessor ComponentAccessor
	settings *factoryOptions

	componentDefinitionsByName map[string]v1alpha1.ComponentDefinition
	childNamesByParent         map[string][]string
}

// NewComponentFactory creates a new Karta-based component factory
func NewComponentFactory(karta *v1alpha1.Karta, accessor ComponentAccessor) *ComponentFactory {
	definitionsByName := make(map[string]v1alpha1.ComponentDefinition)
	childNamesByParent := make(map[string][]string)

	// Create single slice with all components (root + children)
	if karta == nil {
		return &ComponentFactory{accessor: accessor}
	}
	allDefinitions := make([]v1alpha1.ComponentDefinition, 0, len(karta.Spec.StructureDefinition.ChildComponents)+1)
	allDefinitions = append(allDefinitions, karta.Spec.StructureDefinition.ChildComponents...)
	allDefinitions = append(allDefinitions, karta.Spec.StructureDefinition.RootComponent)
	for _, componentDefinition := range allDefinitions {
		definitionsByName[componentDefinition.Name] = componentDefinition
		if componentDefinition.OwnerRef != nil {
			parent := *componentDefinition.OwnerRef
			childNamesByParent[parent] = append(childNamesByParent[parent], componentDefinition.Name)
		}
	}

	return &ComponentFactory{
		karta:                      karta,
		accessor:                   accessor,
		componentDefinitionsByName: definitionsByName,
		childNamesByParent:         childNamesByParent,
	}
}

// FactoryOption configures an object factory and tree.Open.
type FactoryOption func(*factoryOptions)

type factoryOptions struct {
	resolved    references.ResolvedReferences
	hasResolved bool
	reader      references.ResourceReader
	enabled     bool
	costLimit   uint64
	timeout     time.Duration
	evaluation  []celpkg.Option
}

func WithReferences(resolved references.ResolvedReferences) FactoryOption {
	return func(o *factoryOptions) { o.resolved = resolved; o.hasResolved = true }
}

func WithReferenceReader(reader references.ResourceReader) FactoryOption {
	return func(o *factoryOptions) { o.reader = reader }
}

// WithEvaluationLimits bounds each CEL evaluation. Zero keeps the CEL default.
func WithEvaluationLimits(cost uint64, timeout time.Duration) FactoryOption {
	return func(o *factoryOptions) { o.costLimit = cost; o.timeout = timeout }
}

// WithReferencesEnabled disables all reference input when false, including supplied values.
func WithReferencesEnabled(enabled bool) FactoryOption {
	return func(o *factoryOptions) { o.enabled = enabled }
}

func NewComponentFactoryFromObject(karta *v1alpha1.Karta, object KubernetesObject, opts ...FactoryOption) *ComponentFactory {
	options := factoryOptions{enabled: true}
	for _, option := range opts {
		option(&options)
	}
	return newObjectFactory(karta, object, options)
}

func newObjectFactory(karta *v1alpha1.Karta, object KubernetesObject, options factoryOptions) *ComponentFactory {
	fail := func(err error) *ComponentFactory { return NewComponentFactory(karta, NewAccessor(errRunner{err})) }
	if karta == nil || object == nil {
		return fail(errors.New("karta and workload are required"))
	}
	if options.hasResolved && options.reader != nil {
		return fail(errors.New("WithReferences and WithReferenceReader are mutually exclusive"))
	}
	definition := karta.DeepCopy()
	source, err := jsonCopy(object)
	if err != nil {
		return fail(fmt.Errorf("copy workload: %w", err))
	}
	// Freeze supplied objects before evaluating any expressions.
	if options.hasResolved && options.enabled {
		copied, err := options.resolved.Copy()
		if err != nil {
			return fail(err)
		}
		options.resolved = copied
	}
	if options.evaluation == nil {
		plan, err := v1alpha1.ProgramsFor(karta)
		if err != nil {
			return fail(err)
		}
		options.evaluation = []celpkg.Option{celpkg.WithPrograms(plan), celpkg.WithLimits(options.costLimit, options.timeout)}
	}
	reader := references.NewCachingReader(options.reader)
	var mu sync.Mutex
	var cached map[string]any
	provider := func(ctx context.Context) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		if !options.enabled {
			return map[string]any{}, nil
		}
		if cached != nil {
			copy, err := jsonCopy(cached)
			if err != nil {
				return nil, err
			}
			return copy.(map[string]any), nil
		}
		var bindings map[string]any
		switch {
		case options.hasResolved:
			resolved, err := references.ResolveSupplied(ctx, definition.Spec.StructureDefinition.References, source, options.resolved, options.evaluation...)
			if err != nil {
				return nil, err
			}
			bindings, err = resolved.Bindings()
			if err != nil {
				return nil, err
			}
		case options.reader != nil:
			resolved, err := references.ResolveDefinitions(ctx, reader, definition.Spec.StructureDefinition.References, source, options.evaluation...)
			if err != nil {
				return nil, err
			}
			bindings, err = resolved.Bindings()
			if err != nil {
				return nil, err
			}
		default:
			if len(definition.Spec.StructureDefinition.References) != 0 {
				return nil, expression.ErrReferencesNotSupported
			}
			bindings = map[string]any{}
		}
		if bindings == nil {
			bindings = map[string]any{}
		}
		cached = bindings
		copy, err := jsonCopy(cached)
		if err != nil {
			return nil, err
		}
		return copy.(map[string]any), nil
	}
	runnerOptions := append(options.evaluation[:len(options.evaluation):len(options.evaluation)], celpkg.WithReferenceProvider(provider))
	runner, err := celpkg.NewRunnerWithVariables(source, namedExpressions(definition), runnerOptions...)
	if err != nil {
		return fail(err)
	}
	factory := NewComponentFactory(definition, NewAccessor(runner))
	factory.settings = &options
	return factory
}

// ResolveWriteTarget evaluates a destination on the current workload snapshot.
func (f *ComponentFactory) ResolveWriteTarget(ctx context.Context, via *v1alpha1.ValueAccessor, instance string, index int) (WriteTarget, error) {
	accessor, ok := f.accessor.(*Accessor)
	if !ok {
		return WriteTarget{}, errors.New("write paths require an object factory")
	}
	return accessor.ResolveWriteTarget(ctx, via, instance, index)
}

// Mutated returns a fresh factory after applying already-resolved destinations.
// The caller publishes it only after extracting the complete new tree.
func (f *ComponentFactory) Mutated(ctx context.Context, targets []WriteTarget, values []any) (*ComponentFactory, error) {
	return f.mutated(ctx, targets, values, false)
}

// MutatedActions stages definition-owned literal assignments, including JSON null.
func (f *ComponentFactory) MutatedActions(ctx context.Context, targets []WriteTarget, values []any) (*ComponentFactory, error) {
	return f.mutated(ctx, targets, values, true)
}

func (f *ComponentFactory) mutated(ctx context.Context, targets []WriteTarget, values []any, assignment bool) (*ComponentFactory, error) {
	if f.settings == nil {
		return nil, errors.New("mutation requires an object factory")
	}
	object, err := f.accessor.GetObject()
	if err != nil {
		return nil, err
	}
	updated, err := applyWrites(ctx, object, targets, values, assignment)
	if err != nil {
		return nil, err
	}
	if f.settings.enabled && f.settings.hasResolved {
		for _, ref := range f.karta.Spec.StructureDefinition.References {
			before, err := references.Plan(ctx, ref, object, f.settings.evaluation...)
			if err != nil {
				return nil, err
			}
			after, err := references.Plan(ctx, ref, updated, f.settings.evaluation...)
			if err != nil {
				return nil, err
			}
			if !before.Equal(after) {
				return nil, fmt.Errorf("mutation changes reference %q; supply fresh references in a new editor", ref.Name)
			}
		}
	}
	return newObjectFactory(f.karta, &unstructured.Unstructured{Object: updated}, *f.settings), nil
}

func namedExpressions(karta *v1alpha1.Karta) []expression.NamedExpression {
	if karta == nil {
		return nil
	}
	out := make([]expression.NamedExpression, 0, len(karta.Spec.Variables))
	for _, variable := range karta.Spec.Variables {
		out = append(out, expression.NamedExpression{Name: variable.Name, Expression: variable.Expression})
	}

	return out
}

// errRunner is the runner a definition gets when the engine cannot be built: every operation
// returns the construction error instead of evaluating against the wrong document.
type errRunner struct{ err error }

func (r errRunner) Evaluate(context.Context, string) ([]any, error) { return nil, r.err }
func (r errRunner) Assign(context.Context, string, any) error       { return r.err }
func (r errRunner) GetObject() (any, error)                         { return nil, r.err }
func (r errRunner) EvaluateWithVariables(context.Context, string, map[string]any) ([]any, error) {
	return nil, r.err
}
func (r errRunner) EvaluateWritePath(context.Context, string, map[string]any) ([]any, error) {
	return nil, r.err
}
func (r errRunner) ResolveVariables(context.Context, ...string) (map[string]any, error) {
	return nil, r.err
}

// GetComponent retrieves a component by name
func (f *ComponentFactory) GetComponent(name string) (*Component, error) {
	definition, exists := f.componentDefinitionsByName[name]
	if !exists {
		return nil, fmt.Errorf("component %s not found", name)
	}

	return &Component{
		name:       name,
		definition: definition,
		accessor:   f.accessor,
		factory:    f,
	}, nil
}

// GetKarta returns the Karta definition the factory was built from.
func (f *ComponentFactory) GetKarta() *v1alpha1.Karta {
	return f.karta
}

// GetRootComponent retrieves the root component
func (f *ComponentFactory) GetRootComponent() (*Component, error) {
	if f.karta == nil {
		return nil, fmt.Errorf("karta is nil")
	}

	return f.GetComponent(f.karta.Spec.StructureDefinition.RootComponent.Name)
}

// GetChildComponentsOf retrieves the direct child components of the named parent,
// resolved from the structure's OwnerRef relationships.
func (f *ComponentFactory) GetChildComponentsOf(parentName string) ([]*Component, error) {
	names := f.childNamesByParent[parentName]
	children := make([]*Component, 0, len(names))
	for _, name := range names {
		component, err := f.GetComponent(name)
		if err != nil {
			return nil, fmt.Errorf("failed to get child component %s: %w", name, err)
		}
		children = append(children, component)
	}
	return children, nil
}

// GetChildComponents retrieves all child components
func (f *ComponentFactory) GetChildComponents() ([]*Component, error) {
	if f.karta == nil {
		return nil, fmt.Errorf("karta is nil")
	}

	childComponents := make([]*Component, 0, len(f.karta.Spec.StructureDefinition.ChildComponents))
	for _, childDefinition := range f.karta.Spec.StructureDefinition.ChildComponents {
		component, err := f.GetComponent(childDefinition.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to get child component %s: %w", childDefinition.Name, err)
		}
		childComponents = append(childComponents, component)
	}

	return childComponents, nil
}

func (f *ComponentFactory) GetResource() (KubernetesObject, error) {
	object, err := f.accessor.GetObject()
	if err != nil {
		return nil, fmt.Errorf("failed to get updated data: %w", err)
	}
	u := &unstructured.Unstructured{Object: object}
	if err := validateKubernetesObject(u); err != nil {
		return nil, fmt.Errorf("invalid Kubernetes object: %w", err)
	}
	return u, nil
}

func (f *ComponentFactory) IsContainSpecDefinition() (bool, error) {
	components, err := f.GetChildComponents()
	if err != nil {
		return false, fmt.Errorf("failed to get child components: %w", err)
	}
	rootComponent, err := f.GetRootComponent()
	if err != nil {
		return false, fmt.Errorf("failed to get root component: %w", err)
	}
	components = append(components, rootComponent)
	for _, component := range components {
		if isComponentHasSpecDefinition(component.Definition()) {
			return true, nil
		}
	}
	return false, nil
}

func isComponentHasSpecDefinition(componentDefinition v1alpha1.ComponentDefinition) bool {
	if componentDefinition.SpecDefinition == nil {
		return false
	}

	return componentDefinition.SpecDefinition.PodTemplateSpec != nil ||
		componentDefinition.SpecDefinition.PodSpec != nil ||
		componentDefinition.SpecDefinition.FragmentedPodSpecDefinition != nil
}

func validateKubernetesObject(u *unstructured.Unstructured) error {
	gvk := u.GroupVersionKind()
	if gvk.Group == "" && gvk.Version == "" { // Core groups might have empty Group, but need Version
		return fmt.Errorf("missing apiVersion")
	}
	if gvk.Kind == "" {
		return fmt.Errorf("missing kind")
	}
	if u.GetName() == "" && u.GetGenerateName() == "" {
		return fmt.Errorf("missing metadata.name or metadata.generateName")
	}
	return nil
}

func (f *ComponentFactory) applyActions(ctx context.Context, definition v1alpha1.ComponentDefinition, suspend bool) error {
	if definition.SuspendDefinition == nil {
		return nil
	}
	actions := definition.SuspendDefinition.SuspendActions
	if !suspend {
		actions = definition.SuspendDefinition.ResumeActions
	}
	accessor, ok := f.accessor.(*Accessor)
	if !ok {
		return errors.New("actions require an object accessor")
	}
	targets, values, err := accessor.resolveActions(ctx, definition, actions)
	if err != nil {
		return err
	}
	staged, err := f.MutatedActions(ctx, targets, values)
	if err != nil {
		return err
	}
	if _, err = staged.GetResource(); err != nil {
		return err
	}
	f.accessor = staged.accessor
	return nil
}
