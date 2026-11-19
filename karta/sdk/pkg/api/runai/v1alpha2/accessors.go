// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

// Variable is a named expression available through variables.<name>.
type Variable struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-zA-Z_][a-zA-Z0-9_]*$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

// ValueAccessor independently defines reads and writes for a workload field.
// +kubebuilder:validation:MinProperties=1
type ValueAccessor struct {
	// +optional
	Read *ReadAccessor `json:"read,omitempty"`
	// +optional
	Write *WriteAccessor `json:"write,omitempty"`
}

// ReadAccessor reads from the root workload, or the Pod for a Pod selector.
type ReadAccessor struct {
	// +optional
	// +kubebuilder:validation:MinLength=1
	MatchExpression string `json:"matchExpression,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Expression string `json:"expression"`
}

// WriteAccessor selects an RFC 6901 JSON Pointer in the original workload.
// +kubebuilder:validation:XValidation:rule="has(self.path) != has(self.pathExpression)",message="exactly one of path or pathExpression is required"
type WriteAccessor struct {
	// +optional
	// +kubebuilder:validation:MinLength=1
	MatchExpression string `json:"matchExpression,omitempty"`
	// Path is fixed. An empty path selects the root object.
	// +optional
	// +kubebuilder:validation:Pattern="^(/([^~/]|~[01])*)*$"
	Path *string `json:"path,omitempty"`
	// PathExpression computes a JSON Pointer from object, variables, references, instance and index.
	// +optional
	// +kubebuilder:validation:MinLength=1
	PathExpression string `json:"pathExpression,omitempty"`
}
