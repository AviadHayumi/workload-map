// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// CopyJSON copies an object and keeps integral JSON numbers as int64.
func CopyJSON(source any) (any, error) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, errors.New("object is not JSON compatible")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, errors.New("object is not JSON compatible")
	}
	return normalizeNumbers(value)
}
func normalizeNumbers(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i, nil
		}
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return nil, errors.New("invalid JSON number")
		}
		// An integer spelling outside int64 must not silently lose precision.
		if !bytes.ContainsAny([]byte(v), ".eE") {
			return nil, errors.New("JSON integer exceeds int64")
		}
		return f, nil
	case []any:
		for i, x := range v {
			n, err := normalizeNumbers(x)
			if err != nil {
				return nil, err
			}
			v[i] = n
		}
		return v, nil
	case map[string]any:
		for k, x := range v {
			n, err := normalizeNumbers(x)
			if err != nil {
				return nil, err
			}
			v[k] = n
		}
		return v, nil
	case nil, bool, string:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value type %T", v)
	}
}
