// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	engine "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

var bindingName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

type expressionValidator struct {
	variables, references map[string]bool
	errors                []error
}

func (v *expressionValidator) add(path string, err error) {
	if err != nil {
		v.errors = append(v.errors, fmt.Errorf("%s: %w", path, err))
	}
}
func (v *expressionValidator) check(path, source string, scope engine.Scope, predicate bool) {
	if strings.TrimSpace(source) == "" {
		v.add(path, errors.New("expression must not be empty"))
		return
	}
	var err error
	if predicate {
		err = engine.ValidateBool(scope, source)
	} else {
		err = engine.ValidateScope(scope, source)
	}
	v.add(path, err)
	if err != nil || scope == engine.ObjectScope || scope == engine.TargetScope {
		return
	}
	deps, err := engine.Analyze(source)
	v.add(path, err)
	for _, name := range deps.Variables {
		if !v.variables[name] {
			v.add(path, fmt.Errorf("unknown variable %q", name))
		}
	}
	for _, name := range deps.References {
		if !v.references[name] {
			v.add(path, fmt.Errorf("unknown reference %q", name))
		}
	}
}
func (v *expressionValidator) read(path string, read ReadAccessor, scope engine.Scope) {
	v.check(path+".expression", read.Expression, scope, false)
	if read.MatchExpression != "" {
		v.check(path+".matchExpression", read.MatchExpression, scope, true)
	}
}
func (v *expressionValidator) write(path string, write WriteAccessor) {
	if (write.Path == nil) == (write.PathExpression == "") {
		v.add(path, errors.New("exactly one of path or pathExpression is required"))
	}
	if write.Path != nil {
		v.add(path, validateJSONPointer(*write.Path))
	}
	if write.PathExpression != "" {
		v.check(path+".pathExpression", write.PathExpression, engine.WriteScope, false)
	}
	if write.MatchExpression != "" {
		v.check(path+".matchExpression", write.MatchExpression, engine.WriteScope, true)
	}
}
func (v *expressionValidator) accessors(value reflect.Value, path string) {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Type() == reflect.TypeFor[ValueAccessor]() {
		a := value.Interface().(ValueAccessor)
		if a.Read == nil && a.Write == nil {
			v.add(path, errors.New("read or write is required"))
		}
		if a.Read != nil {
			v.read(path+".read", *a.Read, engine.ReadScope)
		}
		if a.Write != nil {
			v.write(path+".write", *a.Write)
		}
		return
	}
	if value.Kind() == reflect.Struct {
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if field.Kind() == reflect.Struct || field.Kind() == reflect.Pointer {
				v.accessors(field, path+"."+value.Type().Field(i).Name)
			}
		}
	}
}
func (v *expressionValidator) component(c ComponentDefinition) {
	path := "components." + c.Name
	v.accessors(reflect.ValueOf(c), path)
	if c.InstanceIds != nil && (c.InstanceIds.Read == nil || c.InstanceIds.Write != nil) {
		v.add(path, errors.New("instanceIds must be read-only"))
	}
	if c.SpecDefinition != nil && c.SpecDefinition.Metadata != nil && c.SpecDefinition.PodSpec == nil {
		v.add(path, errors.New("metadata requires podSpec"))
	}
	if s := c.StatusDefinition; s != nil {
		if s.PhaseDefinition != nil {
			v.read(path+".phaseDefinition.read", s.PhaseDefinition.Read, engine.ReadScope)
		}
		if s.ConditionsDefinition != nil {
			v.read(path+".conditionsDefinition.read", s.ConditionsDefinition.Read, engine.ReadScope)
		}
		for _, entry := range s.StatusMappings.Entries() {
			for _, matcher := range entry.Matchers {
				if matcher.ByExpression != nil {
					v.check(path+".statusMappings."+string(entry.Status), matcher.ByExpression.Expression, engine.ReadScope, true)
				}
			}
		}
	}
	if s := c.SuspendDefinition; s != nil {
		for name, actions := range map[string][]SuspendAction{"suspendActions": s.SuspendActions, "resumeActions": s.ResumeActions} {
			if len(actions) == 0 {
				v.add(path+"."+name, errors.New("actions must not be empty"))
			}
			for i, a := range actions {
				p := fmt.Sprintf("%s.%s[%d]", path, name, i)
				v.write(p+".write", a.Write)
				if !json.Valid([]byte(a.Value)) {
					v.add(p, errors.New("value must be JSON"))
				}
			}
		}
	}
	if s := c.PodSelector; s != nil {
		if s.ComponentTypeSelector != nil {
			v.read(path+".componentTypeSelector.read", s.ComponentTypeSelector.Read, engine.ObjectScope)
		}
		if s.ComponentInstanceSelector != nil {
			v.read(path+".componentInstanceSelector.read", s.ComponentInstanceSelector.Read, engine.ObjectScope)
		}
		if s.ReplicaSelector != nil {
			v.read(path+".replicaSelector.read", s.ReplicaSelector.Read, engine.ObjectScope)
		}
		if s.FromReferences != nil {
			if len(s.FromReferences) == 0 {
				v.add(path, errors.New("fromReferences must not be empty"))
			}
			if s.ComponentTypeSelector == nil {
				v.add(path, errors.New("fromReferences requires componentTypeSelector"))
			}
			seen := map[string]bool{}
			for _, r := range s.FromReferences {
				if !v.references[r.Name] {
					v.add(path, fmt.Errorf("unknown reference %q", r.Name))
				}
				if seen[r.Name] {
					v.add(path, fmt.Errorf("duplicate reference %q", r.Name))
				}
				seen[r.Name] = true
				if r.MatchExpression != "" {
					v.check(path+".fromReferences."+r.Name, r.MatchExpression, engine.TargetScope, true)
				}
			}
		}
	}
}
func (v *expressionValidator) label(path string, value LabelValue) {
	if (value.Value == nil) == (value.Expression == nil) {
		v.add(path, errors.New("exactly one of value or expression is required"))
		return
	}
	if value.Expression != nil {
		v.check(path, *value.Expression, engine.ObjectScope, false)
	} else if len(validation.IsValidLabelValue(*value.Value)) > 0 {
		v.add(path, errors.New("invalid label value"))
	}
}
func (v *expressionValidator) reference(r Reference) {
	path := "references." + r.Name
	if _, err := schema.ParseGroupVersion(r.APIVersion); err != nil || r.APIVersion == "" || r.Kind == "" {
		v.add(path, errors.New("valid apiVersion and kind are required"))
	}
	if (r.Lookup == nil) == (r.List == nil) {
		v.add(path, errors.New("exactly one of lookup or list is required"))
	}
	if r.MatchExpression != "" {
		v.check(path+".matchExpression", r.MatchExpression, engine.ObjectScope, true)
	}
	if r.Lookup != nil {
		v.check(path+".lookup.nameExpression", r.Lookup.NameExpression, engine.ObjectScope, false)
	}
	if list := r.List; list != nil {
		if list.MatchLabels != nil && len(list.MatchLabels) == 0 {
			v.add(path, errors.New("matchLabels must not be empty when present"))
		}
		if list.MatchExpressions != nil && len(list.MatchExpressions) == 0 {
			v.add(path, errors.New("matchExpressions must not be empty when present"))
		}
		if len(list.MatchLabels) == 0 && len(list.MatchExpressions) == 0 {
			v.add(path, errors.New("list requires a label selector"))
		}
		for key, value := range list.MatchLabels {
			if len(validation.IsQualifiedName(key)) > 0 {
				v.add(path, errors.New("invalid label key"))
			}
			v.label(path+".matchLabels."+key, value)
		}
		for _, req := range list.MatchExpressions {
			if len(validation.IsQualifiedName(req.Key)) > 0 {
				v.add(path, errors.New("invalid label key"))
			}
			switch req.Operator {
			case LabelSelectorOpIn, LabelSelectorOpNotIn:
				if len(req.Values) == 0 {
					v.add(path, errors.New("operators In and NotIn require values"))
				}
			case LabelSelectorOpExists, LabelSelectorOpDoesNotExist:
				if len(req.Values) > 0 {
					v.add(path, errors.New("operators Exists and DoesNotExist reject values"))
				}
			default:
				v.add(path, errors.New("invalid label selector operator"))
			}
			for _, value := range req.Values {
				v.label(path+".matchExpressions", value)
			}
		}
	}
}
func (v *KartaValidator) validateExpressions() []error {
	e := expressionValidator{variables: map[string]bool{}, references: map[string]bool{}}
	vars := make([]expression.NamedExpression, 0, len(v.karta.Spec.Variables))
	for _, variable := range v.karta.Spec.Variables {
		vars = append(vars, expression.NamedExpression{Name: variable.Name, Expression: variable.Expression})
		e.variables[variable.Name] = true
	}
	e.add("variables", engine.ValidateVariables(vars))
	for _, ref := range v.karta.Spec.StructureDefinition.References {
		if !bindingName.MatchString(ref.Name) {
			e.add("references", errors.New("name must be a CEL identifier"))
		}
		if e.references[ref.Name] {
			e.add("references", fmt.Errorf("duplicate reference %q", ref.Name))
		}
		e.references[ref.Name] = true
	}
	for _, variable := range v.karta.Spec.Variables {
		e.check("variables."+variable.Name, variable.Expression, engine.ReadScope, false)
	}
	for _, component := range v.allComponents {
		e.component(component)
	}
	for _, ref := range v.karta.Spec.StructureDefinition.References {
		e.reference(ref)
	}
	if g := v.karta.Spec.Instructions.GangScheduling; g != nil {
		for _, group := range g.PodGroups {
			for _, member := range group.Members {
				for _, s := range member.GroupByExpressions {
					e.check("groupByExpressions", s, engine.ObjectScope, false)
				}
				for _, s := range member.Filters {
					e.check("filters", s, engine.ObjectScope, true)
				}
			}
		}
	}
	return e.errors
}
func validateJSONPointer(path string) error {
	if path != "" && !strings.HasPrefix(path, "/") {
		return errors.New("JSON Pointer must be empty or start with /")
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '~' {
			if i+1 >= len(path) || (path[i+1] != '0' && path[i+1] != '1') {
				return errors.New("invalid JSON Pointer escape")
			}
			i++
		}
	}
	return nil
}
