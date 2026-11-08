// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/dsx-ai-factory/workload-map/dsx-workload-exporter/pkg/discovery"
)

// PodWorkloadAssociation changes metric identity for bare Pods whose named target
// includes their current UID in its owner references. It does not change ownership.
type PodWorkloadAssociation struct {
	PodSelector     *metav1.LabelSelector `json:"podSelector" yaml:"podSelector"`
	TargetKind      string                `json:"targetKind" yaml:"targetKind"`
	TargetNameLabel string                `json:"targetNameLabel" yaml:"targetNameLabel"`
}

type podAssociationSnapshot struct {
	eligible   bool
	target     discovery.ObjectRef
	object     *unstructured.Unstructured
	readReason string
}

func (r *Runtime) resolvePodAssociation(ctx context.Context, pod, root *unstructured.Unstructured, workload *discovery.GenericWorkload) (*unstructured.Unstructured, podAssociationSnapshot, watchKey, error) {
	config := r.state.config.PodWorkloadAssociation
	var snapshot podAssociationSnapshot
	if config == nil || workload.Root.GVK != podGVK || workload.Root.UID != pod.GetUID() || workload.Resolution != discovery.Resolved || !r.state.associationSelector.Matches(labelsFor(pod)) {
		return root, snapshot, watchKey{}, nil
	}
	owner, valid := discovery.ControllerOwner(pod)
	name := pod.GetLabels()[config.TargetNameLabel]
	if !valid || owner.UID != "" || name == "" {
		return root, snapshot, watchKey{}, nil
	}
	snapshot.eligible = true
	selection, err := r.state.lookupDefinition(r.state.associationTarget)
	if err != nil {
		return root, snapshot, watchKey{}, err
	}
	// Association uses an already selected full-object collection. It never
	// introduces a watch or gives a metadata-only collection workload semantics.
	if selection.Definition == nil || selection.GVK != r.state.associationTarget {
		return root, snapshot, watchKey{}, nil
	}
	selected, err := r.state.isResourceSelected(ctx, r.state.associationTarget)
	if err != nil || !selected {
		return root, snapshot, watchKey{}, err
	}
	snapshot.target = discovery.ObjectRef{GVK: r.state.associationTarget, Namespace: pod.GetNamespace(), Name: name}
	object, source, err := r.state.reader.read(snapshot.target, true)
	snapshot.object, snapshot.readReason = object, failureReason(err)
	if apierrors.IsNotFound(err) {
		return root, snapshot, source, nil
	}
	if err != nil {
		return root, snapshot, source, err
	}
	owner, valid = discovery.ControllerOwner(object)
	if !valid || owner.UID != "" || object.GetNamespace() != pod.GetNamespace() || !r.state.objectSelector.Matches(labelsFor(object)) {
		return root, snapshot, source, nil
	}
	for _, ref := range object.GetOwnerReferences() {
		if ref.APIVersion == "v1" && ref.Kind == "Pod" && ref.Name == pod.GetName() && ref.UID == pod.GetUID() {
			return object, snapshot, source, nil
		}
	}
	return root, snapshot, source, nil
}

func ValidatePodWorkloadAssociation(config *PodWorkloadAssociation) error {
	if config == nil {
		return nil
	}
	if _, err := parseAssociationTarget(config.TargetKind); err != nil {
		return err
	}
	if errors := validation.IsQualifiedName(config.TargetNameLabel); len(errors) > 0 {
		return fmt.Errorf("podWorkloadAssociation.targetNameLabel: %s", strings.Join(errors, ", "))
	}
	if _, err := selector(config.PodSelector); err != nil {
		return fmt.Errorf("podWorkloadAssociation.podSelector: %w", err)
	}
	return nil
}

func parseAssociationTarget(value string) (schema.GroupVersionKind, error) {
	separator := strings.LastIndexByte(value, '/')
	if separator > 0 && separator < len(value)-1 && !strings.ContainsAny(value, " \t\r\n*") && value[0] != '/' {
		gv, err := schema.ParseGroupVersion(value[:separator])
		if err == nil && gv.Version != "" {
			gvk := gv.WithKind(value[separator+1:])
			if gvk != podGVK && gvk != namespaceGVK && gvk != kartaGVK {
				return gvk, nil
			}
		}
	}
	return schema.GroupVersionKind{}, fmt.Errorf("podWorkloadAssociation.targetKind must name a workload as group/version/Kind or version/Kind: %q", value)
}
