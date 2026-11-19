// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build js && wasm

package main

import (
	"encoding/json"
	"errors"
	"syscall/js"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

// A uniform result envelope gives every TypeScript caller the same unwrap path.
func encodeEnvelope(result any, resultErr error) js.Value {
	envelope := map[string]any{"data": nil, "error": nil}
	if resultErr != nil {
		envelope["error"] = resultErr.Error()
		if errors.Is(resultErr, expression.ErrReferencesNotSupported) {
			envelope["needsReferences"] = true
		}
		return js.ValueOf(envelope)
	}

	encodedResult, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		envelope["error"] = marshalErr.Error()
		return js.ValueOf(envelope)
	}

	envelope["data"] = string(encodedResult)
	return js.ValueOf(envelope)
}
