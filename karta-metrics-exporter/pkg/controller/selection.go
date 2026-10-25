// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ResourceRule struct {
	APIGroups   []string `json:"apiGroups"`
	APIVersions []string `json:"apiVersions"`
	Resources   []string `json:"resources"`
}

func ValidateResourceRules(rules []ResourceRule) error {
	for index, rule := range rules {
		if len(rule.APIGroups) == 0 || len(rule.APIVersions) == 0 || len(rule.Resources) == 0 {
			return fmt.Errorf("resource rule %d requires apiGroups, apiVersions and resources", index)
		}
		for _, version := range rule.APIVersions {
			if version == "" || strings.ContainsAny(version, "/ \t\n") {
				return errors.New("resource rule API versions must name a version or *")
			}
		}
		for _, resource := range rule.Resources {
			if resource == "" || strings.ContainsAny(resource, "/ \t\n") {
				return errors.New("resource rules must name whole resources or *")
			}
		}
	}
	return nil
}

func matchesResource(rule ResourceRule, gvr schema.GroupVersionResource) bool {
	return (slices.Contains(rule.APIGroups, "*") || slices.Contains(rule.APIGroups, gvr.Group)) && (slices.Contains(rule.APIVersions, "*") || slices.Contains(rule.APIVersions, gvr.Version)) && (slices.Contains(rule.Resources, "*") || slices.Contains(rule.Resources, gvr.Resource))
}

func (r *Runtime) selectedRoot(object *unstructured.Unstructured) (bool, error) {
	if !r.objectSelector.Matches(labelsFor(object)) {
		return false, nil
	}
	if len(r.config.ResourceRules) == 0 && len(r.config.ExcludeResourceRules) == 0 {
		return true, nil
	}
	gvk := object.GroupVersionKind()
	mapping, err := r.reader.clients.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return false, err
	}
	for _, rule := range r.config.ExcludeResourceRules {
		if matchesResource(rule, mapping.Resource) {
			return false, nil
		}
	}
	if len(r.config.ResourceRules) == 0 {
		return true, nil
	}
	for _, rule := range r.config.ResourceRules {
		if matchesResource(rule, mapping.Resource) {
			return true, nil
		}
	}
	return false, nil
}
