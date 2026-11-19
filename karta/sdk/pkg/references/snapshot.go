// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package references

import (
	"errors"
	"fmt"

	"sigs.k8s.io/yaml"
)

// DecodeSnapshot decodes offline lookup and list results indexed by Karta
// reference name. An empty map is a complete input with no matches.
func DecodeSnapshot(data []byte) (ResolvedReferences, error) {
	var refs ResolvedReferences
	if err := yaml.UnmarshalStrict(data, &refs); err != nil {
		return nil, fmt.Errorf("decode supplied references: %w", err)
	}
	if refs == nil {
		return nil, errors.New("expected a map of supplied references")
	}
	return refs, nil
}
