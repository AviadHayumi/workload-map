// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package pkg

import (
	"encoding/json"
	"testing"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestSubmitterAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, user, raw, wantType string
		wantPatch                 bool
	}{
		{"original caller", "alice@example.test", `{"metadata":{"annotations":{"workload.dgxc.nvidia.com/submitter-id":"forged","other":"preserve"}}}`, "unknown", true},
		{"user-supplied owner", "alice@example.test", `{"metadata":{"ownerReferences":[{"apiVersion":"batch/v1","kind":"Job","name":"untrusted","uid":"untrusted"}]}}`, "unknown", true},
		{"service account", "system:serviceaccount:test:submitter", `{"metadata":{}}`, "service", true},
		{"controller child", "system:kube-controller-manager", `{"metadata":{"ownerReferences":[{"apiVersion":"apps/v1","kind":"ReplicaSet","name":"owner","uid":"owner"}]}}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &SubmitterAdmission{Source: "kubernetes:test"}
			req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Create, UserInfo: authv1.UserInfo{Username: tc.user}, Object: runtime.RawExtension{Raw: []byte(tc.raw)}}}
			response := h.Handle(t.Context(), req)
			if !response.Allowed {
				t.Fatalf("denied: %v", response.Result)
			}
			if (len(response.Patches) > 0) != tc.wantPatch {
				t.Fatalf("patches: %v", response.Patches)
			}
			if !tc.wantPatch {
				return
			}
			encoded, err := json.Marshal(response.Patches)
			if err != nil {
				t.Fatal(err)
			}
			patch, err := jsonpatch.DecodePatch(encoded)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := patch.Apply([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			var obj struct {
				Metadata struct{ Annotations map[string]string }
			}
			if err := json.Unmarshal(updated, &obj); err != nil {
				t.Fatal(err)
			}
			if obj.Metadata.Annotations[submitterPrefix+"submitter-id"] != tc.user || obj.Metadata.Annotations[submitterPrefix+"submitter-type"] != tc.wantType {
				t.Fatal("incorrect principal")
			}
			v := &SubmitterAdmission{Validate: true, Source: "kubernetes:test"}
			req.Operation = admissionv1.Update
			req.OldObject.Raw = updated
			req.Object.Raw = []byte(`{"metadata":{}}`)
			if v.Handle(t.Context(), req).Allowed {
				t.Fatal("identity removal allowed")
			}
			req.Object.Raw = updated
			if !v.Handle(t.Context(), req).Allowed {
				t.Fatal("unchanged identity denied")
			}
		})
	}
}
