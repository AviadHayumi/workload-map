// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

import (
	"testing"

	"k8s.io/utils/ptr"
)

func TestCELDefinitionContract(t *testing.T) {
	valid := func() *Karta {
		return &Karta{Spec: KartaSpec{StructureDefinition: StructureDefinition{RootComponent: ComponentDefinition{Name: "root", Kind: &GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, StatusDefinition: &StatusDefinition{}, ScaleDefinition: &ScaleDefinition{Replicas: &ValueAccessor{Read: &ReadAccessor{Expression: "object.spec.replicas"}, Write: &WriteAccessor{Path: ptr.To("/spec/replicas")}}}}}}}
	}
	tests := []struct {
		name   string
		change func(*Karta)
		valid  bool
	}{
		{"nested read write", func(*Karta) {}, true},
		{"explicit supported TypeMeta", func(k *Karta) { k.APIVersion = "run.ai/v1alpha2"; k.Kind = "Karta" }, true},
		{"legacy jq API version", func(k *Karta) { k.APIVersion = "run.ai/v1alpha1"; k.Kind = "Karta" }, false},
		{"unknown API group", func(k *Karta) { k.APIVersion = "other.example/v1alpha2"; k.Kind = "Karta" }, false},
		{"wrong kind", func(k *Karta) { k.APIVersion = "run.ai/v1alpha2"; k.Kind = "KartaList" }, false},

		{"write only", func(k *Karta) { k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Read = nil }, true},
		{"read only", func(k *Karta) { k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Write = nil }, true},
		{"empty accessor", func(k *Karta) { k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas = &ValueAccessor{} }, false},
		{"root write", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Write.Path = ptr.To("")
		}, true},
		{"pointer missing slash", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Write.Path = ptr.To("spec/x")
		}, false},
		{"pointer bad escape", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Write.Path = ptr.To("/spec/~2")
		}, false},
		{"two destinations", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Write.PathExpression = "'/spec/x'"
		}, false},
		{"read match numeric", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Read.MatchExpression = "1"
		}, false},
		{"write value unavailable", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Write.MatchExpression = "value > 0"
		}, false},
		{"read index unavailable", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Read.Expression = "index"
		}, false},
		{"status numeric", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.StatusDefinition.StatusMappings.Running = []StatusMatcher{{ByExpression: &ExpressionMatcher{Expression: "1"}}}
		}, false},
		{"unknown variable", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Read.Expression = "variables.unknown"
		}, false},
		{"unknown reference", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.ScaleDefinition.Replicas.Read.Expression = "references.unknown"
		}, false},
		{"pod root-only", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.PodSelector = &PodSelector{ComponentTypeSelector: &ComponentTypeSelector{Read: ReadAccessor{Expression: "references.runtime"}}}
		}, false},
		{"empty membership refs", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.PodSelector = &PodSelector{FromReferences: []PodReference{}}
		}, false},
		{"unknown membership ref", func(k *Karta) {
			k.Spec.StructureDefinition.RootComponent.PodSelector = &PodSelector{ComponentTypeSelector: &ComponentTypeSelector{Read: ReadAccessor{Expression: "true"}}, FromReferences: []PodReference{{Name: "unknown"}}}
		}, false},
		{"reference missing lookup", func(k *Karta) {
			k.Spec.StructureDefinition.References = []Reference{{Name: "runtime", APIVersion: "v1", Kind: "Pod"}}
		}, false},
		{"reference chained lookup", func(k *Karta) {
			k.Spec.StructureDefinition.References = []Reference{{Name: "runtime", APIVersion: "v1", Kind: "Pod", Lookup: &LookupReference{NameExpression: "references.other.metadata.name"}}}
		}, false},
		{"reference empty list", func(k *Karta) {
			k.Spec.StructureDefinition.References = []Reference{{Name: "runtime", APIVersion: "v1", Kind: "Pod", List: &ListReference{}}}
		}, false},
		{"explicit empty labels beside a valid requirement", func(k *Karta) {
			k.Spec.StructureDefinition.References = []Reference{{Name: "runtime", APIVersion: "v1", Kind: "Pod", List: &ListReference{MatchLabels: map[string]LabelValue{}, MatchExpressions: []LabelSelectorRequirement{{Key: "app", Operator: LabelSelectorOpExists}}}}}
		}, false},
		{"explicit empty requirements beside a valid label", func(k *Karta) {
			k.Spec.StructureDefinition.References = []Reference{{Name: "runtime", APIVersion: "v1", Kind: "Pod", List: &ListReference{MatchLabels: map[string]LabelValue{"app": {Value: ptr.To("worker")}}, MatchExpressions: []LabelSelectorRequirement{}}}}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := valid()
			tc.change(k)
			err := NewKartaValidator(k).Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
