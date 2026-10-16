// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package store

import (
	"github.com/dsx-ai-factory/workload-map/exporter/pkg/identity"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type IdentityRecord struct {
	InputHash [32]byte
	UID       types.UID
	Ref       WorkloadRef
	Result    identity.Result
}

func (s *Store) UpsertIdentity(record IdentityRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identities == nil {
		s.identities = make(map[types.UID]IdentityRecord)
	}
	s.identities[record.UID] = record
}

func (s *Store) DeleteIdentity(uid types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.identities, uid)
}

func (s *Store) DeleteIdentitiesForKind(gvk schema.GroupVersionKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for uid, record := range s.identities {
		if record.Ref.Group == gvk.Group && record.Ref.Version == gvk.Version && record.Ref.Kind == gvk.Kind {
			delete(s.identities, uid)
		}
	}
}

func (s *Store) Identity(uid types.UID) (IdentityRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.identities[uid]
	return record, ok
}
