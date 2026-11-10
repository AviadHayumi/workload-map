// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"bytes"
	"encoding/json"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

type state struct {
	Version     int           `json:"version"`
	WorkloadUID types.UID     `json:"workloadUID"`
	Pool        string        `json:"pool,omitempty"`
	Service     bool          `json:"service,omitempty"`
	Storage     *storageState `json:"storage,omitempty"`
}
type storageState struct {
	Size         string    `json:"size"`
	StorageClass string    `json:"storageClass"`
	UID          types.UID `json:"uid,omitempty"`
}

func readState(object *unstructured.Unstructured) (state, error) {
	result := state{Version: 1, WorkloadUID: object.GetUID()}
	raw, present := object.GetAnnotations()[annotationState]
	if !present {
		return result, nil
	}
	if len(raw) > 4096 || len(raw) == 0 {
		return result, failure("InvalidState", "state must contain at most 4096 bytes of versioned JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return result, failure("InvalidState", "state must be a JSON object")
	}
	for key, value := range fields {
		switch key {
		case "version", "workloadUID", "pool", "service", "storage":
		default:
			return result, failure("InvalidState", "state contains unknown fields")
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return result, failure("InvalidState", "state fields cannot be null")
		}
	}
	if storage, present := fields["storage"]; present {
		var saved map[string]json.RawMessage
		if err := json.Unmarshal(storage, &saved); err != nil || saved == nil {
			return result, failure("InvalidState", "saved storage must be an object")
		}
		if _, present := saved["size"]; !present {
			return result, failure("InvalidState", "saved storage requires size")
		}
		if _, present := saved["storageClass"]; !present {
			return result, failure("InvalidState", "saved storage requires storageClass")
		}
		for key, value := range saved {
			switch key {
			case "size", "storageClass", "uid":
			default:
				return result, failure("InvalidState", "saved storage contains unknown fields")
			}
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return result, failure("InvalidState", "saved storage fields cannot be null")
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	// Decode into a fresh value so absent mandatory fields cannot inherit defaults.
	var decoded state
	if err := decoder.Decode(&decoded); err != nil {
		return result, failure("InvalidState", "state is malformed or contains unknown fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, failure("InvalidState", "state must contain one JSON object")
	}
	if decoded.Version != 1 || decoded.WorkloadUID != object.GetUID() || decoded.WorkloadUID == "" {
		return result, failure("InvalidState", "state version or workload UID does not match")
	}
	if decoded.Pool != "" && len(validation.IsValidLabelValue(decoded.Pool)) != 0 {
		return result, failure("InvalidState", "saved node pool is invalid")
	}
	if decoded.Storage != nil {
		intent, err := decodeStorage(map[string]any{"size": decoded.Storage.Size})
		if err != nil || intent.Size != decoded.Storage.Size {
			return result, failure("InvalidState", "saved storage size is invalid or noncanonical")
		}
		if decoded.Storage.StorageClass != "" && len(validation.IsDNS1123Subdomain(decoded.Storage.StorageClass)) != 0 {
			return result, failure("InvalidState", "saved storage class is invalid")
		}
	}
	return decoded, nil
}

func saveState(object *unstructured.Unstructured, value state) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 4096 {
		return failure("InvalidState", "state cannot be encoded within 4096 bytes")
	}
	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[annotationState] = string(data)
	object.SetAnnotations(annotations)
	return nil
}

func editTemplate(object *unstructured.Unstructured, path []string, previous state, desired *plan, next *state) error {
	next.Pool = ""
	if desired.Placement != nil {
		next.Pool = desired.Placement.Pool
	}
	next.Service = desired.Service != nil
	fields := []struct {
		path                []string
		key, old, new       string
		wasOwned, wantOwned bool
	}{
		{append(append([]string{}, path...), "spec", "nodeSelector"), poolKey, previous.Pool, next.Pool, previous.Pool != "", desired.Placement != nil},
		{append(append([]string{}, path...), "metadata", "labels"), workloadUIDKey, string(object.GetUID()), string(object.GetUID()), previous.Service, desired.Service != nil},
	}
	for _, field := range fields {
		current, present, err := unstructured.NestedFieldNoCopy(object.Object, append(field.path, field.key)...)
		if err != nil {
			return failure("FieldConflict", "reserved template field has an incompatible parent map")
		}
		if field.wasOwned {
			if !present || current != field.old {
				return failure("FieldConflict", "a previously managed template field was removed or changed")
			}
		} else if field.wantOwned && present {
			return failure("FieldConflict", "a reserved template key already exists")
		}
		if field.wantOwned {
			if err := unstructured.SetNestedField(object.Object, field.new, append(field.path, field.key)...); err != nil {
				return failure("FieldConflict", "cannot write reserved template field")
			}
		} else if field.wasOwned {
			unstructured.RemoveNestedField(object.Object, append(field.path, field.key)...)
		}
	}
	return nil
}
