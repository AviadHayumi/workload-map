// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package v1alpha2

import (
	"runtime"
	"testing"
)

func TestProgramsFollowSpecAcrossObservations(t *testing.T) {
	def := &Karta{Spec: KartaSpec{Variables: []Variable{{Name: "replicas", Expression: "2"}}}}
	copy := def.DeepCopy()
	first, err := ProgramsFor(def)
	if err != nil {
		t.Fatal(err)
	}
	copy.ResourceVersion = "3"
	second, err := ProgramsFor(copy)
	if err != nil || second != first {
		t.Fatalf("spec copy recompiled after metadata change: %v", err)
	}
	def.Spec.Variables[0].Expression = "3"
	updated, err := ProgramsFor(def)
	if err != nil || updated == first {
		t.Fatalf("new spec reused previous programs: %v", err)
	}
	def.Spec.Variables[0].Expression = "2"
	restored, err := ProgramsFor(def)
	if err != nil || restored != second {
		t.Fatalf("old active revision recompiled: %v", err)
	}
	runtime.KeepAlive(copy)
}
