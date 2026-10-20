// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"fmt"
	"strings"
)

// ValidateResourceRules checks main-resource rules with whole-field wildcards.
func ValidateResourceRules(rules []ResourceRule) error {
	for i, rule := range rules {
		for _, field := range []struct {
			name       string
			values     []string
			allowEmpty bool
		}{
			{"apiGroups", rule.APIGroups, true},
			{"apiVersions", rule.APIVersions, false},
			{"resources", rule.Resources, false},
		} {
			if len(field.values) == 0 {
				return fmt.Errorf("resource rule %d: %s must not be empty", i, field.name)
			}
			for j, value := range field.values {
				if value == "*" || value == "" && field.allowEmpty {
					continue
				}
				if value == "" || strings.ContainsAny(value, "*/ \t\r\n") {
					return fmt.Errorf("resource rule %d: invalid %s[%d] %q; use a main-resource value or whole-field wildcard", i, field.name, j, value)
				}
			}
		}
	}
	return nil
}
