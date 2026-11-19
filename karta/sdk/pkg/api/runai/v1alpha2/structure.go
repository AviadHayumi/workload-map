// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

// GroupVersionKind represents a Kubernetes API object's group, version, and kind.
type GroupVersionKind struct {
	// Group is the API group of the resource
	Group string `json:"group"`

	// Version is the API version of the resource
	Version string `json:"version"`

	// Kind is the API kind of the resource
	Kind string `json:"kind"`
}

// ComponentDefinition defines a single component in the workload hierarchy.
// Components represent logical units of computation that can be optimized independently.
type ComponentDefinition struct {
	// Name is the unique identifier for this component within the Karta
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Kind specifies the Kubernetes GroupVersionKind for this component
	// +kubebuilder:validation:Optional
	Kind *GroupVersionKind `json:"kind,omitempty"`

	// OwnerRef references the parent component in the hierarchy by name (nil for root component)
	// +kubebuilder:validation:Optional
	OwnerRef *string `json:"ownerRef,omitempty"`

	// SpecDefinition defines how to extract pod specifications from this component
	// +kubebuilder:validation:Optional
	SpecDefinition *SpecDefinition `json:"specDefinition,omitempty"`

	// ScaleDefinition defines how to extract scaling information from this component
	// +kubebuilder:validation:Optional
	ScaleDefinition *ScaleDefinition `json:"scaleDefinition,omitempty"`

	// StatusDefinition defines how to interpret the status of this component
	// should be added only for the root component
	// +kubebuilder:validation:Optional
	StatusDefinition *StatusDefinition `json:"statusDefinition,omitempty"`

	// SuspendDefinition defines the path/value assignments used to suspend and resume this component.
	// Should only be populated for components where the underlying framework provides native, first-class support for suspension primitives.
	// (e.g. spec.suspend for batch/v1 Job).
	// +kubebuilder:validation:Optional
	SuspendDefinition *SuspendDefinition `json:"suspendDefinition,omitempty"`

	// InstanceIds reads component instance identifiers. Writes are not allowed.
	// +kubebuilder:validation:XValidation:rule="has(self.read) && !has(self.write)",message="instanceIds must be read-only"
	// +kubebuilder:validation:Optional
	InstanceIds *ValueAccessor `json:"instanceIds,omitempty"`

	// PodSelector defines how to identify pods belonging to this component
	// +kubebuilder:validation:Optional
	PodSelector *PodSelector `json:"podSelector,omitempty"`
}

// SuspendDefinition defines the field assignments applied to a manifest when
// suspending or resuming a workload. Each action specifies the target field path
// and the JSON value to apply atomically through the SDK editor.
type SuspendDefinition struct {
	// SuspendActions is an ordered list of path/value assignments applied on suspend.
	// Actions are applied in sequence; later actions may overwrite earlier ones.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	SuspendActions []SuspendAction `json:"suspendActions"`

	// ResumeActions is an ordered list of path/value assignments applied on resume.
	// Actions are applied in sequence; later actions may overwrite earlier ones.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	ResumeActions []SuspendAction `json:"resumeActions"`
}

// SuspendAction is a single field assignment applied during a suspend or resume operation.
type SuspendAction struct {
	// Write locates the field to change.
	// +kubebuilder:validation:Required
	Write WriteAccessor `json:"write"`

	// Value is the JSON-encoded value to assign at the path (e.g. "true", "0", `"paused"`, "null").
	// +kubebuilder:validation:Required
	Value string `json:"value"`
}

// SpecDefinition defines how to extract pod specifications from a component.
// Only one of the three options should be provided (PodTemplateSpec, FragmentedPodSpec, PodSpec + Metadata).
type SpecDefinition struct {
	// PodTemplateSpec reads and writes a complete PodTemplateSpec object
	// +kubebuilder:validation:Optional
	PodTemplateSpec *ValueAccessor `json:"podTemplateSpec,omitempty"`

	// PodSpec reads and writes a complete PodSpec object
	// +kubebuilder:validation:Optional
	PodSpec *ValueAccessor `json:"podSpec,omitempty"`

	// Metadata reads and writes the component metadata
	// May be used only with PodSpec, in cases where pod spec and metadata are separated
	// +kubebuilder:validation:Optional
	Metadata *ValueAccessor `json:"metadata,omitempty"`

	// FragmentedPodSpecDefinition defines how to extract individual pod spec fields
	// when they are scattered across different paths in the component
	// +kubebuilder:validation:Optional
	FragmentedPodSpecDefinition *FragmentedPodSpecDefinition `json:"fragmentedPodSpecDefinition,omitempty"`
}

// FragmentedPodSpecDefinition defines CEL read expressions to individual pod spec fields
// when they are scattered across different locations in the component YAML.
type FragmentedPodSpecDefinition struct {
	// SchedulerName reads and writes the scheduler name
	// +kubebuilder:validation:Optional
	SchedulerName *ValueAccessor `json:"schedulerName,omitempty"`

	// Labels reads and writes pod labels
	// +kubebuilder:validation:Optional
	Labels *ValueAccessor `json:"labels,omitempty"`

	// Annotations reads and writes pod annotations
	// +kubebuilder:validation:Optional
	Annotations *ValueAccessor `json:"annotations,omitempty"`

	// Resources reads and writes resource requirements
	// +kubebuilder:validation:Optional
	Resources *ValueAccessor `json:"resources,omitempty"`

	// ResourceClaims reads and writes DRA resource claims
	// +kubebuilder:validation:Optional
	ResourceClaims *ValueAccessor `json:"resourceClaims,omitempty"`

	// PodAffinity reads and writes pod affinity rules
	// +kubebuilder:validation:Optional
	PodAffinity *ValueAccessor `json:"podAffinity,omitempty"`

	// NodeAffinity reads and writes node affinity rules
	// +kubebuilder:validation:Optional
	NodeAffinity *ValueAccessor `json:"nodeAffinity,omitempty"`

	// Containers reads and writes containers specifications
	// +kubebuilder:validation:Optional
	Containers *ValueAccessor `json:"containers,omitempty"`

	// Container reads and writes a single container specification.
	// Used when the component has only one container
	// +kubebuilder:validation:Optional
	Container *ValueAccessor `json:"container,omitempty"`

	// PriorityClassName reads and writes the priority class name
	// +kubebuilder:validation:Optional
	PriorityClassName *ValueAccessor `json:"priorityClassName,omitempty"`

	// Image reads and writes the container image
	// +kubebuilder:validation:Optional
	Image *ValueAccessor `json:"image,omitempty"`
}

// ScaleDefinition defines how to extract scaling information from a component.
type ScaleDefinition struct {
	// Replicas reads and writes the current replica count
	// +kubebuilder:validation:Optional
	Replicas *ValueAccessor `json:"replicas,omitempty"`

	// MinReplicas reads and writes the minimum replica count
	// +kubebuilder:validation:Optional
	MinReplicas *ValueAccessor `json:"minReplicas,omitempty"`

	// MaxReplicas reads and writes the maximum replica count
	// +kubebuilder:validation:Optional
	MaxReplicas *ValueAccessor `json:"maxReplicas,omitempty"`
}

// PodSelector defines how to identify pods belonging to a specific component.
// +kubebuilder:validation:XValidation:rule="!has(self.fromReferences) || has(self.componentTypeSelector)",message="fromReferences requires a componentTypeSelector"
type PodSelector struct {
	// FromReferences adds eligible referenced objects as Pod membership targets.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	FromReferences []PodReference `json:"fromReferences,omitempty"`

	// ComponentTypeSelector determines whether a pod belongs to this component type
	// by reading a Pod label or annotation.
	// This is the primary mechanism for pod-to-component membership.
	// For example, LWS "leader" uses worker-index="0" to identify leader pods,
	// and LWS "worker" checks for the existence of the leader-name annotation.
	// +kubebuilder:validation:Optional
	ComponentTypeSelector *ComponentTypeSelector `json:"componentTypeSelector,omitempty"`

	// ComponentInstanceSelector splits a single ComponentDefinition into multiple
	// component instances, each identified by a unique value extracted from the pod.
	// Use this when instances of the same component type represent fundamentally
	// different roles or services (e.g., Dynamo "service" definition produces
	// separate "Frontend", "PrefillWorker", "DecodeWorker" instances).
	// This is distinct from ReplicaSelector: ComponentInstanceSelector creates
	// instances with potentially different specs/behavior, while ReplicaSelector
	// creates replicas of an identical sub-structure.
	// +kubebuilder:validation:Optional
	ComponentInstanceSelector *ComponentInstanceSelector `json:"componentInstanceSelector,omitempty"`

	// ReplicaSelector identifies which replica index or group the pod belongs to.
	// Use this to distinguish between multiple replicas of the same component / component instance
	// sub-structure (e.g., LWS Group 0 vs Group 1, where each group has identical
	// leader/worker descendants). Descendant components automatically inherit the replica
	// context from their ancestor (parent), so ReplicaSelector should only be defined at
	// the level where replicas are created, not repeated in descendants.
	// +kubebuilder:validation:Optional
	ReplicaSelector *ReplicaSelector `json:"replicaSelector,omitempty"`
}

type ComponentTypeSelector struct {
	// Read extracts the identifying key/label on the pod
	// CEL read expression is evaluated against individual pod objects, not the root resource spec
	// +kubebuilder:validation:Required
	Read ReadAccessor `json:"read"`

	// Value is the expected value for the key (optional - if nil, only key existence is checked)
	// +kubebuilder:validation:Optional
	Value *string `json:"value,omitempty"`
}

type ComponentInstanceSelector struct {
	// Read extracts the component instance identifier on the pod
	// CEL read expression is evaluated against individual pod objects, not the root resource spec
	// +kubebuilder:validation:Required
	Read ReadAccessor `json:"read"`
}

// ReplicaSelector identifies the replica index/group a pod belongs to.
type ReplicaSelector struct {
	// Read extracts the replica identifier on the pod.
	// The evaluated result should be the replica index or group identifier (e.g., "0", "1").
	// +kubebuilder:validation:Required
	Read ReadAccessor `json:"read"`
}

// ResourceStatus represents the high-level status of a component.
// +kubebuilder:validation:Enum=Initializing;Running;Completed;Failed;Degraded;Undefined;Suspended;Suspending;Resuming
type ResourceStatus string

const (
	// InitializingStatus indicates the component has been created or starting up or preparing to run (pre Running status)
	InitializingStatus ResourceStatus = "Initializing"

	// RunningStatus indicates the component is actively running
	RunningStatus ResourceStatus = "Running"

	// CompletedStatus indicates the component has finished successfully
	CompletedStatus ResourceStatus = "Completed"

	// FailedStatus indicates the component has failed
	FailedStatus ResourceStatus = "Failed"

	// DegradedStatus indicates the component is running but in a degraded state
	DegradedStatus ResourceStatus = "Degraded"

	// UndefinedStatus is used when status was not defined or cannot be determined
	UndefinedStatus ResourceStatus = "Undefined"

	// SuspendedStatus indicates the component is fully suspended
	SuspendedStatus ResourceStatus = "Suspended"

	// SuspendingStatus indicates the component is in the process of being suspended (draining pods)
	SuspendingStatus ResourceStatus = "Suspending"

	// ResumingStatus indicates the component is in the process of being resumed
	ResumingStatus ResourceStatus = "Resuming"
)

// StatusDefinition defines how to interpret the status of a component.
type StatusDefinition struct {
	// PhaseDefinition defines how to extract a simple phase/state string
	// +kubebuilder:validation:Optional
	PhaseDefinition *PhaseDefinition `json:"phaseDefinition,omitempty"`

	// ConditionsDefinition defines how to extract Kubernetes-style conditions
	// +kubebuilder:validation:Optional
	ConditionsDefinition *ConditionsDefinition `json:"conditionsDefinition,omitempty"`

	// StatusMappings define how to map extracted status to ResourceStatus values
	// +kubebuilder:validation:Required
	StatusMappings StatusMappings `json:"statusMappings"`
}

// PhaseDefinition defines how to extract a simple phase/state string from the component.
type PhaseDefinition struct {
	// Read extracts the phase/state field
	// +kubebuilder:validation:Required
	Read ReadAccessor `json:"read"`
}

// ConditionsDefinition defines how to extract Kubernetes-style conditions from the component.
type ConditionsDefinition struct {
	// Read extracts the conditions array
	// +kubebuilder:validation:Required
	Read ReadAccessor `json:"read"`

	// TypeFieldName is the field name for the condition type
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=type
	TypeFieldName string `json:"typeFieldName"`

	// StatusFieldName is the field name for the condition status
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=status
	StatusFieldName string `json:"statusFieldName"`

	// MessageFieldName is the field name for the condition text message
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=message
	MessageFieldName *string `json:"messageFieldName,omitempty"`

	// ReasonFieldName is the field name for the condition reason
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=reason
	ReasonFieldName *string `json:"reasonFieldName,omitempty"`
}

// StatusMappings define how to map extracted status information to ResourceStatus values.
// Each status field contains an array of matchers evaluated with OR logic:
// if ANY matcher in the array succeeds, that status is matched.
type StatusMappings struct {
	// Initializing defines matchers for the Initializing status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Initializing []StatusMatcher `json:"initializing,omitempty"`

	// Running defines matchers for the Running status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Running []StatusMatcher `json:"running,omitempty"`

	// Completed defines matchers for the Completed status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Completed []StatusMatcher `json:"completed,omitempty"`

	// Failed defines matchers for the Failed status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Failed []StatusMatcher `json:"failed,omitempty"`

	// Degraded defines matchers for the Degraded status.
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Degraded []StatusMatcher `json:"degraded,omitempty"`

	// Suspended defines matchers for the Suspended status
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Suspended []StatusMatcher `json:"suspended,omitempty"`

	// Suspending defines matchers for the Suspending status
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Suspending []StatusMatcher `json:"suspending,omitempty"`

	// Resuming defines matchers for the Resuming status
	// Multiple matchers are OR'd together.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	Resuming []StatusMatcher `json:"resuming,omitempty"`
}

// StatusMatchEntry pairs a ResourceStatus with its matchers.
type StatusMatchEntry struct {
	Status   ResourceStatus
	Matchers []StatusMatcher
}

// Entries returns all status-to-matchers pairs defined in the mappings.
// When adding a new ResourceStatus, add a corresponding entry here.
func (m StatusMappings) Entries() []StatusMatchEntry {
	return []StatusMatchEntry{
		{ResumingStatus, m.Resuming},
		{SuspendingStatus, m.Suspending},
		{SuspendedStatus, m.Suspended},
		{RunningStatus, m.Running},
		{FailedStatus, m.Failed},
		{CompletedStatus, m.Completed},
		{InitializingStatus, m.Initializing},
		{DegradedStatus, m.Degraded},
	}
}

// StatusMatcher defines criteria for matching a specific status.
// If both ByPhase and ByConditions are provided, ALL must match (AND logic).
type StatusMatcher struct {
	// ByPhase matches against a specific phase value
	// +kubebuilder:validation:Optional
	ByPhase string `json:"byPhase,omitempty"`

	// ByConditions matches against specific condition combinations (ANDed together) at least one of status or reason must be used.
	// +kubebuilder:validation:Optional
	// +listType=atomic
	ByConditions []ExpectedCondition `json:"byConditions,omitempty"`

	// ByExpression is a CEL expression that matches against the object for status matching
	// +kubebuilder:validation:Optional
	ByExpression *ExpressionMatcher `json:"byExpression,omitempty"`
}

// ExpressionMatcher defines a boolean CEL expression for status matching.
type ExpressionMatcher struct {
	// Expression is the CEL expression to evaluate
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

// ExpectedCondition defines a condition type and status that must be present.
type ExpectedCondition struct {
	// Type is the condition type to match
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Status is the expected condition status
	// +kubebuilder:validation:Optional
	Status *string `json:"status,omitempty"`

	// Reason is the expected condition reason
	// +kubebuilder:validation:Optional
	Reason *string `json:"reason,omitempty"`
}
