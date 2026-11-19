// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"weak"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
)

type revisionKey struct {
	digest   [sha256.Size]byte
	programs weak.Pointer[cel.Programs]
}

var revisions = struct {
	sync.Mutex
	bySpec map[[sha256.Size]byte]weak.Pointer[cel.Programs]
	// Keep programs alive while any caller owns the definition, including
	// between observations when no workload factory is holding the programs.
	byObject map[weak.Pointer[Karta]]*cel.Programs
}{bySpec: make(map[[sha256.Size]byte]weak.Pointer[cel.Programs]), byObject: make(map[weak.Pointer[Karta]]*cel.Programs)}

// ProgramsFor shares checked CEL for the lifetime of an observed definition.
// A spec digest also identifies local definitions without a resourceVersion,
// spec changes made in place, and separate decodes of the same revision.
func ProgramsFor(definition *Karta) (*cel.Programs, error) {
	if definition == nil {
		return nil, errors.New("karta is nil")
	}
	spec, err := json.Marshal(definition.Spec)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(spec)
	owner := weak.Make(definition)
	revisions.Lock()
	defer revisions.Unlock()
	programs := revisions.bySpec[digest].Value()
	if programs == nil {
		programs = new(cel.Programs)
		slot := weak.Make(programs)
		revisions.bySpec[digest] = slot
		runtime.AddCleanup(programs, releaseRevision, revisionKey{digest, slot})
	}
	if _, exists := revisions.byObject[owner]; !exists {
		runtime.AddCleanup(definition, releaseDefinition, owner)
	}
	revisions.byObject[owner] = programs
	return programs, nil
}

func releaseDefinition(owner weak.Pointer[Karta]) {
	revisions.Lock()
	defer revisions.Unlock()
	delete(revisions.byObject, owner)
}

func releaseRevision(revision revisionKey) {
	revisions.Lock()
	defer revisions.Unlock()
	if revisions.bySpec[revision.digest] == revision.programs {
		delete(revisions.bySpec, revision.digest)
	}
}
