// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package admission_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"
)

const submitterAnnotation = "karta.run.ai/submitted-by"

// This test always starts a private control plane. No ambient kubeconfig is read.
func TestNativeSubmitterPolicies(t *testing.T) {
	assets := os.Getenv("KARTA_ADMISSION_ENVTEST_ASSETS")
	if assets == "" {
		t.Skip("set KARTA_ADMISSION_ENVTEST_ASSETS to local Kubernetes 1.34+ envtest binaries")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Fatal("helm is required")
	}
	existing := false
	preserve := true
	env := &envtest.Environment{BinaryAssetsDirectory: assets, UseExistingCluster: &existing, CRDs: []*apiextensionsv1.CustomResourceDefinition{{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.admission.example.com"},
		Spec:       apiextensionsv1.CustomResourceDefinitionSpec{Group: "admission.example.com", Scope: apiextensionsv1.NamespaceScoped, Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets", Singular: "widget", Kind: "Widget"}, Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true, Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}}, Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object", Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object", XPreserveUnknownFields: &preserve}, "status": {Type: "object", XPreserveUnknownFields: &preserve}}}}}}},
	}}}
	if os.Getenv("KARTA_ADMISSION_ENVTEST_BETA") == "true" {
		env.ControlPlane.GetAPIServer().Configure().Set("feature-gates", "MutatingAdmissionPolicy=true").Set("runtime-config", "admissionregistration.k8s.io/v1beta1=true")
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	userCfg := rest.CopyConfig(cfg)
	userCfg.Impersonate = rest.ImpersonationConfig{UserName: "alice", Groups: []string{"system:masters"}}
	alice, err := dynamic.NewForConfig(userCfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, ns := range []string{"captured", "outside"} {
		labels := map[string]string{}
		if ns == "captured" {
			labels["karta.run.ai/capture-submitter"] = "true"
		}
		if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	resources, err := client.Discovery().ServerPreferredResources()
	if err != nil {
		t.Fatal(err)
	}
	var apiArgs []string
	for _, list := range resources {
		for _, r := range list.APIResources {
			apiArgs = append(apiArgs, "--api-versions", list.GroupVersion+"/"+r.Kind)
		}
	}
	policyResources := map[string]string{"MutatingAdmissionPolicy": "mutatingadmissionpolicies", "MutatingAdmissionPolicyBinding": "mutatingadmissionpolicybindings", "ValidatingAdmissionPolicy": "validatingadmissionpolicies", "ValidatingAdmissionPolicyBinding": "validatingadmissionpolicybindings"}
	render := func(enabled bool, constraints map[string]any) []*unstructured.Unstructured {
		t.Helper()
		values := map[string]any{"operator": map[string]any{"enabled": false}, "webhook": map[string]any{"enabled": false}, "crdUpgrader": map[string]any{"enabled": false}, "submitterAdmission": map[string]any{"enabled": enabled, "matchConstraints": constraints, "matchConditions": []any{map[string]any{"name": "not-skipped", "expression": "!has(object.metadata.labels) || !('skip' in object.metadata.labels)"}}}}
		input, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		args := append([]string{"template", "native", "../../../charts/karta", "-f", "-"}, apiArgs...)
		cmd := exec.CommandContext(ctx, "helm", args...)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helm: %v\n%s", err, out)
		}
		var docs []*unstructured.Unstructured
		for _, raw := range strings.Split(string(out), "\n---") {
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(raw), &obj); err != nil {
				t.Fatal(err)
			}
			u := &unstructured.Unstructured{Object: obj}
			if policyResources[u.GetKind()] != "" {
				docs = append(docs, u)
			}
		}
		return docs
	}
	installed := map[schema.GroupVersionResource][]string{}
	install := func(docs []*unstructured.Unstructured) {
		t.Helper()
		next := map[schema.GroupVersionResource][]string{}
		for _, doc := range docs {
			gvr := doc.GroupVersionKind().GroupVersion().WithResource(policyResources[doc.GetKind()])
			old, err := admin.Resource(gvr).Get(ctx, doc.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				_, err = admin.Resource(gvr).Create(ctx, doc, metav1.CreateOptions{})
			} else if err == nil {
				doc.SetResourceVersion(old.GetResourceVersion())
				_, err = admin.Resource(gvr).Update(ctx, doc, metav1.UpdateOptions{})
			}
			if err != nil {
				t.Fatalf("install %s: %v", doc.GetKind(), err)
			}
			next[gvr] = append(next[gvr], doc.GetName())
		}
		for gvr, names := range installed {
			for _, name := range names {
				found := false
				for _, keep := range next[gvr] {
					if keep == name {
						found = true
					}
				}
				if !found {
					if err := admin.Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		installed = next
	}
	rule := func(group, resource string) map[string]any {
		return map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{group}, "apiVersions": []any{"v1"}, "resources": []any{resource}, "scope": "Namespaced"}
	}
	constraints := map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"karta.run.ai/capture-submitter": "true"}}, "resourceRules": []any{rule("", "configmaps"), rule("", "pods"), rule("admission.example.com", "widgets")}, "excludeResourceRules": []any{map[string]any{"operations": []any{"CREATE"}, "apiGroups": []any{""}, "apiVersions": []any{"v1"}, "resources": []any{"configmaps"}, "resourceNames": []any{"excluded"}, "scope": "Namespaced"}}}
	install(render(true, constraints))
	configmaps := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	widgets := schema.GroupVersionResource{Group: "admission.example.com", Version: "v1", Resource: "widgets"}
	object := func(kind, name string, annotations map[string]string) *unstructured.Unstructured {
		version := "v1"
		if kind == "Widget" {
			version = "admission.example.com/v1"
		}
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name}}}
		u.SetAnnotations(annotations)
		return u
	}
	// Admission configuration propagates asynchronously; dry runs avoid persisting a pre-policy object.
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := alice.Resource(configmaps).Namespace("captured").Create(ctx, object("ConfigMap", "ready", nil), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err != nil {
			return false, nil
		}
		if got.GetAnnotations()[submitterAnnotation] != "alice" {
			return false, nil
		}
		_, err = alice.Resource(configmaps).Namespace("captured").Create(ctx, object("ConfigMap", "spoof-probe", map[string]string{submitterAnnotation: "mallory"}), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return err != nil && strings.Contains(err.Error(), "authenticated caller"), nil
	}); err != nil {
		t.Fatal("capture never became ready:", err)
	}
	for _, tc := range []struct {
		name, namespace, identity string
		supplied, skipped, denied bool
	}{
		{name: "missing", namespace: "captured", identity: "alice"},
		{name: "matching", namespace: "captured", identity: "alice", supplied: true},
		{name: "spoof", namespace: "captured", identity: "mallory", supplied: true, denied: true},
		{name: "empty", namespace: "captured", supplied: true, denied: true},
		{name: "outside", namespace: "outside"},
		{name: "excluded", namespace: "captured"},
		{name: "condition", namespace: "captured", skipped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			annotations := map[string]string{"unrelated": "preserved"}
			if tc.supplied {
				annotations[submitterAnnotation] = tc.identity
			}
			u := object("ConfigMap", tc.name, annotations)
			if tc.skipped {
				u.SetLabels(map[string]string{"skip": "true"})
			}
			got, err := alice.Resource(configmaps).Namespace(tc.namespace).Create(ctx, u, metav1.CreateOptions{})
			if tc.denied {
				if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
					t.Fatalf("expected admission denial, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.GetAnnotations()[submitterAnnotation] != tc.identity || got.GetAnnotations()["unrelated"] != "preserved" {
				t.Fatalf("unexpected annotations %v", got.GetAnnotations())
			}
		})
	}
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := alice.Resource(widgets).Namespace("captured").Create(ctx, object("Widget", "widget-ready", nil), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return err == nil && got.GetAnnotations()[submitterAnnotation] == "alice", nil
	}); err != nil {
		t.Fatal("CRD capture never became ready:", err)
	}
	serviceCfg := rest.CopyConfig(userCfg)
	serviceCfg.Impersonate.UserName = "system:serviceaccount:captured:submitter"
	serviceClient, err := dynamic.NewForConfig(serviceCfg)
	if err != nil {
		t.Fatal(err)
	}
	serviceObject, err := serviceClient.Resource(configmaps).Namespace("captured").Create(ctx, object("ConfigMap", "service-account", nil), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if serviceObject.GetAnnotations()[submitterAnnotation] != serviceCfg.Impersonate.UserName {
		t.Fatal("service principal was not captured")
	}
	widget, err := alice.Resource(widgets).Namespace("captured").Create(ctx, object("Widget", "protected", nil), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().ServiceAccounts("captured").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	podObject := object("Pod", "protected", nil)
	podObject.Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "main", "image": "registry.k8s.io/pause:3.10"}}}
	pod, err := alice.Resource(pods).Namespace("captured").Create(ctx, podObject, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertProtection := func(stage string) {
		t.Helper()
		for _, subresource := range []string{"", "status"} {
			for _, remove := range []bool{false, true} {
				update := widget.DeepCopy()
				resource := widgets
				if subresource == "status" {
					update = pod.DeepCopy()
					resource = pods
				}
				update.SetLabels(map[string]string{"skip": "true"})
				if remove {
					update.SetAnnotations(nil)
				} else {
					update.SetAnnotations(map[string]string{submitterAnnotation: "mallory"})
				}
				var err error
				if subresource == "" {
					_, err = alice.Resource(resource).Namespace("captured").Update(ctx, update, metav1.UpdateOptions{})
				} else {
					_, err = alice.Resource(resource).Namespace("captured").UpdateStatus(ctx, update, metav1.UpdateOptions{})
				}
				if !apierrors.IsInvalid(err) && !apierrors.IsForbidden(err) {
					t.Fatalf("%s %s remove=%v should deny identity change: %v", stage, subresource, remove, err)
				}
				if !strings.Contains(err.Error(), "recorded submitter") {
					t.Fatalf("unexpected rejection: %v", err)
				}
			}
		}
		statusUpdate := pod.DeepCopy()
		annotations := statusUpdate.GetAnnotations()
		annotations["unrelated"] = stage
		statusUpdate.SetAnnotations(annotations)
		pod, err = alice.Resource(pods).Namespace("captured").UpdateStatus(ctx, statusUpdate, metav1.UpdateOptions{})
		if err != nil || pod.GetAnnotations()["unrelated"] != stage {
			t.Fatalf("unrelated metadata via Pod/status should pass: %v", err)
		}
		updated := widget.DeepCopy()
		updated.SetLabels(map[string]string{"other": "allowed"})
		var err error
		widget, err = alice.Resource(widgets).Namespace("captured").Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			t.Fatalf("ordinary update should pass: %v", err)
		}
	}
	// Wait for the protection binding to become effective as well.
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		u := widget.DeepCopy()
		u.SetAnnotations(nil)
		_, err := alice.Resource(widgets).Namespace("captured").Update(ctx, u, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
		return err != nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	assertProtection("capture enabled")
	ns, err := client.CoreV1().Namespaces().Get(ctx, "captured", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns.Labels = map[string]string{}
	if _, err := client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	constraints["resourceRules"] = []any{rule("", "configmaps")}
	install(render(true, constraints))
	assertProtection("namespace and resource scope narrowed")
	install(render(false, constraints))
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		got, err := alice.Resource(widgets).Namespace("captured").Create(ctx, object("Widget", "disabled-probe", nil), metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		return err == nil && got.GetAnnotations()[submitterAnnotation] == "", nil
	}); err != nil {
		t.Fatal(err)
	}
	assertProtection("capture disabled")
	t.Logf("native admission verified against %s", assets)
}
