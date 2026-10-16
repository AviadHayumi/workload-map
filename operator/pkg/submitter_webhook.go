// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package pkg

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SubmitterAdmission records the principal of a top-level CREATE request.
// Controller-created children retain their platform metadata and use root joins.
type SubmitterAdmission struct {
	Source   string
	Validate bool
}

const submitterPrefix = "workload.dgxc.nvidia.com/"

func (h *SubmitterAdmission) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.SubResource != "" {
		return admission.Allowed("subresource")
	}
	var obj unstructured.Unstructured
	if err := json.Unmarshal(req.Object.Raw, &obj.Object); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if h.Validate {
		if req.Operation != admissionv1.Update {
			return admission.Allowed("not an update")
		}
		var old unstructured.Unstructured
		if err := json.Unmarshal(req.OldObject.Raw, &old.Object); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if len(old.GetOwnerReferences()) != 0 && old.GetAnnotations()[submitterPrefix+"identity-source"] != h.Source {
			return admission.Allowed("child resource")
		}
		for _, key := range []string{"submitter-id", "identity-source", "submitter-type"} {
			before, after := old.GetAnnotations()[submitterPrefix+key], obj.GetAnnotations()[submitterPrefix+key]
			if before != after {
				return admission.Denied("original submitter annotations are immutable in the selected scope")
			}
		}
		return admission.Allowed("original submitter unchanged")
	}
	if req.Operation != admissionv1.Create || (len(obj.GetOwnerReferences()) != 0 && strings.HasPrefix(req.UserInfo.Username, "system:")) {
		return admission.Allowed("not a top-level creation")
	}
	if req.UserInfo.Username == "" || h.Source == "" {
		return admission.Denied("submitter principal and configured identity source are required")
	}
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	principalType := "unknown"
	if strings.HasPrefix(req.UserInfo.Username, "system:") {
		principalType = "service"
	}
	for key, want := range map[string]string{"submitter-id": req.UserInfo.Username, "identity-source": h.Source, "submitter-type": principalType} {
		if existing, ok := annotations[submitterPrefix+key]; ok && existing != want {
			return admission.Denied("existing submitter annotations conflict with the authenticated principal")
		}
	}
	annotations[submitterPrefix+"submitter-id"] = req.UserInfo.Username
	annotations[submitterPrefix+"identity-source"] = h.Source
	annotations[submitterPrefix+"submitter-type"] = principalType
	obj.SetAnnotations(annotations)
	data, err := json.Marshal(obj.Object)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, data)
}
