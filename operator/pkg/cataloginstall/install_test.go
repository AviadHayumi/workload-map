// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package cataloginstall

import (
	"context"
	"errors"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog/kartas"
)

func testClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, admissionv1.AddToScheme, extv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
}
func TestInstallIdempotentAndUpdatesOwned(t *testing.T) {
	c := testClient(t)
	definition := kartas.Deployment()
	bundle := []*api.Karta{definition}
	for range 2 {
		if err := Install(t.Context(), c, "test/release", bundle); err != nil {
			t.Fatal(err)
		}
	}
	current := &api.Karta{}
	if err := c.Get(t.Context(), types.NamespacedName{Name: definition.Name}, current); err != nil {
		t.Fatal(err)
	}
	if current.Annotations[OwnerAnnotation] != "test/release" {
		t.Fatal("missing catalog owner")
	}
	definition.Spec.Experimental = true
	if err := Install(t.Context(), c, "test/release", bundle); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(t.Context(), types.NamespacedName{Name: definition.Name}, current); err != nil {
		t.Fatal(err)
	}
	if !current.Spec.Experimental {
		t.Fatal("owned catalog was not updated")
	}
}
func TestConflictPreflightWritesNothing(t *testing.T) {
	for _, sameName := range []bool{true, false} {
		t.Run(map[bool]string{true: "same name", false: "different name same workload"}[sameName], func(t *testing.T) {
			custom := kartas.Deployment()
			if !sameName {
				custom.Name = "custom-deployment"
			}
			c := testClient(t, custom)
			err := Install(t.Context(), c, "test/release", []*api.Karta{kartas.BatchJob(), kartas.Deployment()})
			if err == nil {
				t.Fatal("custom conflict accepted")
			}
			objects := &api.KartaList{}
			if err := c.List(t.Context(), objects); err != nil {
				t.Fatal(err)
			}
			if len(objects.Items) != 1 {
				t.Fatal("wrote before complete preflight")
			}
		})
	}
}
func TestPartialInstallRetry(t *testing.T) {
	base := testClient(t)
	failure := errors.New("transient write failure")
	calls := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		calls++
		if calls == 2 {
			return failure
		}
		return c.Create(ctx, obj, opts...)
	}})
	bundle := []*api.Karta{kartas.BatchJob(), kartas.Deployment()}
	if err := Install(t.Context(), c, "test/release", bundle); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if err := Install(t.Context(), c, "test/release", bundle); err != nil {
		t.Fatal(err)
	}
	list := &api.KartaList{}
	if err := c.List(t.Context(), list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatal("retry did not finish")
	}
}
func TestAdmissionReadiness(t *testing.T) {
	crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "kartas.run.ai"}, Spec: extv1.CustomResourceDefinitionSpec{Versions: []extv1.CustomResourceDefinitionVersion{{Name: "v1alpha2", Served: true}}}, Status: extv1.CustomResourceDefinitionStatus{Conditions: []extv1.CustomResourceDefinitionCondition{{Type: extv1.Established, Status: extv1.ConditionTrue}}}}
	webhook := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "validation"}, Webhooks: []admissionv1.ValidatingWebhook{{Name: "vkarta.run.ai", FailurePolicy: ptr.To(admissionv1.Fail), ClientConfig: admissionv1.WebhookClientConfig{CABundle: []byte("test"), Service: &admissionv1.ServiceReference{Path: ptr.To("/validate-run-ai-v1alpha2-karta")}}, Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update}, Rule: admissionv1.Rule{APIGroups: []string{"run.ai"}, APIVersions: []string{"v1alpha2"}, Resources: []string{"kartas"}}}}}}}
	for _, tc := range []struct {
		name       string
		version    string
		operations [][]admissionv1.OperationType // one list per rule
		ready      bool
	}{
		{"ready", "v1alpha2", [][]admissionv1.OperationType{{admissionv1.Create, admissionv1.Update}}, true},
		{"all operations", "v1alpha2", [][]admissionv1.OperationType{{admissionv1.OperationAll}}, true},
		{"operations in separate rules", "v1alpha2", [][]admissionv1.OperationType{{admissionv1.Create}, {admissionv1.Update}}, false},
		{"wrong API version", "v1alpha1", [][]admissionv1.OperationType{{admissionv1.Create, admissionv1.Update}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := webhook.DeepCopy()
			rule := hook.Webhooks[0].Rules[0]
			rule.APIVersions = []string{tc.version}
			hook.Webhooks[0].Rules = nil
			for _, ops := range tc.operations {
				rule.Operations = ops
				hook.Webhooks[0].Rules = append(hook.Webhooks[0].Rules, rule)
			}
			base := testClient(t, crd, hook)
			dryRuns := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				options := &client.CreateOptions{}
				for _, opt := range opts {
					opt.ApplyToCreate(options)
				}
				if len(options.DryRun) != 1 || options.DryRun[0] != metav1.DryRunAll {
					t.Fatal("readiness must be dry-run")
				}
				dryRuns++
				return nil
			}})
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			defer cancel()
			err := WaitReady(ctx, c, "validation", kartas.Deployment())
			if tc.ready && (err != nil || dryRuns != 1) {
				t.Fatalf("ready err=%v dryRuns=%d", err, dryRuns)
			}
			if !tc.ready && (err == nil || dryRuns != 0) {
				t.Fatalf("unready err=%v dryRuns=%d", err, dryRuns)
			}
		})
	}
}
