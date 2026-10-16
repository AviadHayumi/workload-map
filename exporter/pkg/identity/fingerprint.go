// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"maps"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Fingerprint covers every CEL input; status-only updates reuse the prior result.
func Fingerprint(obj metav1.Object, gvk schema.GroupVersionKind) [32]byte {
	h := sha256.New()
	for _, value := range []string{obj.GetNamespace(), obj.GetName(), gvk.Group, gvk.Version, gvk.Kind} {
		writeField(h, value)
	}
	for _, fields := range []map[string]string{obj.GetLabels(), obj.GetAnnotations()} {
		var count [8]byte
		binary.LittleEndian.PutUint64(count[:], uint64(len(fields)))
		_, _ = h.Write(count[:])
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			writeField(h, key)
			writeField(h, fields[key])
		}
	}
	return [32]byte(h.Sum(nil))
}

func writeField(h hash.Hash, value string) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}
