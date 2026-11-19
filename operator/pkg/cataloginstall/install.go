// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package cataloginstall installs bundled definitions after admission is ready.
package cataloginstall

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

const OwnerAnnotation = "karta.run.ai/catalog-owner"

// WaitReady requires the v1alpha2 CRD and fail-closed validating webhook. A
// dry-run request proves the API server can call admission before any real write.
func WaitReady(ctx context.Context, c client.Client, webhookName string, probe *api.Karta) error {
	if webhookName == "" || probe == nil {
		return errors.New("catalog readiness requires a webhook name and probe")
	}
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := c.Get(ctx, types.NamespacedName{Name: "kartas.run.ai"}, crd); err != nil {
			return false, nil
		}
		established, served := false, false
		for _, condition := range crd.Status.Conditions {
			if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
				established = true
			}
		}
		for _, version := range crd.Spec.Versions {
			if version.Name == "v1alpha2" && version.Served {
				served = true
			}
		}
		if !established || !served {
			return false, nil
		}
		config := &admissionv1.ValidatingWebhookConfiguration{}
		if err := c.Get(ctx, types.NamespacedName{Name: webhookName}, config); err != nil {
			return false, nil
		}
		ready := false
		for _, hook := range config.Webhooks {
			if hook.FailurePolicy == nil || *hook.FailurePolicy != admissionv1.Fail || len(hook.ClientConfig.CABundle) == 0 {
				continue
			}
			if hook.ClientConfig.Service == nil || hook.ClientConfig.Service.Path == nil || *hook.ClientConfig.Service.Path != "/validate-run-ai-v1alpha2-karta" {
				continue
			}
			for _, rule := range hook.Rules {
				hasGroup, hasVersion, hasResource, hasCreate, hasUpdate := false, false, false, false, false
				for _, g := range rule.APIGroups {
					hasGroup = hasGroup || g == "run.ai"
				}
				for _, v := range rule.APIVersions {
					hasVersion = hasVersion || v == "v1alpha2"
				}
				for _, r := range rule.Resources {
					hasResource = hasResource || r == "kartas"
				}
				for _, op := range rule.Operations {
					hasCreate = hasCreate || op == admissionv1.Create || op == admissionv1.OperationAll
					hasUpdate = hasUpdate || op == admissionv1.Update || op == admissionv1.OperationAll
				}
				ready = ready || hasGroup && hasVersion && hasResource && hasCreate && hasUpdate
			}
		}
		if !ready {
			return false, nil
		}
		test := &api.Karta{}
		err := c.Get(ctx, types.NamespacedName{Name: probe.Name}, test)
		switch {
		case err == nil:
			// Preserve its name and resourceVersion so the uniqueness check
			// excludes this same definition during upgrades and retries.
			if err := c.Update(ctx, test, client.DryRunAll); err != nil {
				return false, nil
			}
		case apierrors.IsNotFound(err):
			if err := c.Create(ctx, probe.DeepCopy(), client.DryRunAll); err != nil {
				return false, nil
			}
		default:
			return false, nil
		}
		return true, nil
	})
}

// Install preflights the complete bundle before writing. A custom definition
// claiming any bundled GVK is a conflict, even if it uses another name. Updates
// use resourceVersion; concurrent edits fail and can be retried as a fresh run.
// A failed run may have installed a prefix; rerunning is idempotent.
func Install(ctx context.Context, c client.Client, owner string, bundle []*api.Karta) error {
	byName, err := preflight(ctx, c, owner, bundle)
	if err != nil {
		return err
	}
	for _, definition := range bundle {
		desired := definition.DeepCopy()
		if desired.Annotations == nil {
			desired.Annotations = map[string]string{}
		}
		desired.Annotations[OwnerAnnotation] = owner
		current := byName[desired.Name]
		if current == nil {
			if err := c.Create(ctx, desired); err != nil {
				return fmt.Errorf("create catalog definition %q: %w", desired.Name, err)
			}
			continue
		}
		if reflect.DeepEqual(current.Spec, desired.Spec) {
			continue
		}
		next := current.DeepCopy()
		next.Spec = desired.Spec
		if err := c.Update(ctx, next); err != nil {
			if apierrors.IsConflict(err) {
				return fmt.Errorf("catalog definition %q changed concurrently; retry the installation: %w", desired.Name, err)
			}
			return fmt.Errorf("update catalog definition %q: %w", desired.Name, err)
		}
	}
	return nil
}

// CheckConflicts validates the bundle and rejects observed custom definitions
// before the installer waits for admission. Install repeats this check before
// writing; admission uniqueness remains best-effort under concurrent creates.
func CheckConflicts(ctx context.Context, c client.Client, owner string, bundle []*api.Karta) error {
	_, err := preflight(ctx, c, owner, bundle)
	return err
}

func preflight(ctx context.Context, c client.Client, owner string, bundle []*api.Karta) (map[string]*api.Karta, error) {
	if owner == "" {
		return nil, errors.New("catalog owner must not be empty")
	}
	existing := &api.KartaList{}
	if err := c.List(ctx, existing); err != nil {
		return nil, fmt.Errorf("list definitions: %w", err)
	}
	byName := map[string]*api.Karta{}
	for i := range existing.Items {
		item := &existing.Items[i]
		byName[item.Name] = item
	}
	seenNames := map[string]bool{}
	seenGVKs := map[string]bool{}
	for _, definition := range bundle {
		if definition == nil {
			return nil, errors.New("nil catalog definition")
		}
		if err := api.NewKartaValidator(definition).Validate(); err != nil {
			return nil, fmt.Errorf("catalog definition %q: %w", definition.Name, err)
		}
		if seenNames[definition.Name] {
			return nil, fmt.Errorf("duplicate catalog name %q", definition.Name)
		}
		seenNames[definition.Name] = true
		key := catalog.RootKey(definition)
		if seenGVKs[key.String()] {
			return nil, fmt.Errorf("duplicate catalog workload type %s", key)
		}
		seenGVKs[key.String()] = true
		for i := range existing.Items {
			current := &existing.Items[i]
			if current.Name != definition.Name && catalog.RootKey(current) != key {
				continue
			}
			if current.Name != definition.Name || catalog.RootKey(current) != key || current.Annotations[OwnerAnnotation] != owner {
				return nil, fmt.Errorf("catalog conflict with custom definition %q", current.Name)
			}
		}
	}
	return byName, nil
}
