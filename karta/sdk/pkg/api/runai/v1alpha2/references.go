// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

// Reference declares another resource (or set of resources) whose values are
// exposed to every component's expressions as references.<name>.
// Exactly one of Lookup or List is set:
//
//	Lookup -> references.<name> is a single object (absent when not found)
//	List   -> references.<name> is a list (possibly empty)
//
// A namespaced reference always resolves in the workload's own namespace; there is
// deliberately no namespace field.
// +kubebuilder:validation:XValidation:rule="has(self.lookup) != has(self.list)",message="exactly one of lookup or list must be set"
type Reference struct {
	// Name is the variable name the reference is exposed under, as references.<name>.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-zA-Z_][a-zA-Z0-9_]*$"
	Name string `json:"name"`

	// APIVersion identifies the referenced resource API.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`

	// Kind identifies the referenced resource kind.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// MatchExpression enables this reference when true, using the root object.
	// +optional
	// +kubebuilder:validation:MinLength=1
	MatchExpression string `json:"matchExpression,omitempty"`

	// Lookup fetches a single object by name.
	// +optional
	Lookup *LookupReference `json:"lookup,omitempty"`

	// List fetches a set of objects by a structured label selector.
	// +optional
	List *ListReference `json:"list,omitempty"`
}

// LookupReference fetches a single resource by name.
type LookupReference struct {
	// NameExpression is a CEL expression against the root object resolving the
	// referenced resource's name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	NameExpression string `json:"nameExpression"`
}

// ListReference fetches a set of resources by a structured label selector. After value
// resolution it maps onto a Kubernetes label selector. At least one of MatchLabels or
// MatchExpressions must be set.
// +kubebuilder:validation:MinProperties=1
type ListReference struct {
	// MatchLabels selects resources whose labels equal each resolved value.
	// +optional
	// +kubebuilder:validation:MinProperties=1
	MatchLabels map[string]LabelValue `json:"matchLabels,omitempty"`

	// MatchExpressions selects resources by label selector requirements.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	MatchExpressions []LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

// LabelSelectorRequirement mirrors the Kubernetes selector requirement, except its
// values may be sourced from the root object.
// +kubebuilder:validation:XValidation:rule="self.operator in ['In', 'NotIn'] ? (has(self.values) && size(self.values) > 0) : (!has(self.values) || size(self.values) == 0)",message="selector values must match the operator"
type LabelSelectorRequirement struct {
	// Key is the label key the requirement applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`

	// Operator is the requirement's relationship to its values.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=In;NotIn;Exists;DoesNotExist
	Operator LabelSelectorOperator `json:"operator"`

	// Values are required for In/NotIn and must be empty for Exists/DoesNotExist.
	// +optional
	// +listType=atomic
	Values []LabelValue `json:"values,omitempty"`
}

// LabelSelectorOperator is the set of operators a selector requirement can use.
type LabelSelectorOperator string

const (
	LabelSelectorOpIn           LabelSelectorOperator = "In"
	LabelSelectorOpNotIn        LabelSelectorOperator = "NotIn"
	LabelSelectorOpExists       LabelSelectorOperator = "Exists"
	LabelSelectorOpDoesNotExist LabelSelectorOperator = "DoesNotExist"
)

// LabelValue is a single label value: either a literal (Value) or a CEL expression
// evaluated against the root object (Expression). Exactly one is set.
// +kubebuilder:validation:MinProperties=1
// +kubebuilder:validation:MaxProperties=1
type LabelValue struct {
	// Value is a literal label value.
	// +optional
	Value *string `json:"value,omitempty"`

	// Expression is a CEL expression against the root object resolving the value.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Expression *string `json:"expression,omitempty"`
}

// PodReference names a reference used to find this component's Pods.
type PodReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// MatchExpression evaluates the root object and each resolved target.
	// +optional
	// +kubebuilder:validation:MinLength=1
	MatchExpression string `json:"matchExpression,omitempty"`
}
