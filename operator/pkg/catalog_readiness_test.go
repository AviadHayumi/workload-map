// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package pkg

import (
	"context"
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
	"github.com/dsx-ai-factory/workload-map/operator/pkg/cataloginstall"
)

func TestCatalogReadinessUsesExistingIdentityOnUpgrade(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial install", true: "upgrade"}[existing], func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, extv1.AddToScheme, admissionv1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			definition := kartas.BatchJob()
			if err := (&KartaLabeler{}).Default(t.Context(), definition); err != nil {
				t.Fatal(err)
			}
			crd := &extv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: "kartas.run.ai"}, Spec: extv1.CustomResourceDefinitionSpec{Versions: []extv1.CustomResourceDefinitionVersion{{Name: "v1alpha2", Served: true}}}, Status: extv1.CustomResourceDefinitionStatus{Conditions: []extv1.CustomResourceDefinitionCondition{{Type: extv1.Established, Status: extv1.ConditionTrue}}}}
			hook := &admissionv1.ValidatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "validation"}, Webhooks: []admissionv1.ValidatingWebhook{{Name: "vkarta.run.ai", FailurePolicy: ptr.To(admissionv1.Fail), ClientConfig: admissionv1.WebhookClientConfig{CABundle: []byte("test"), Service: &admissionv1.ServiceReference{Path: ptr.To("/validate-run-ai-v1alpha2-karta")}}, Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update}, Rule: admissionv1.Rule{APIGroups: []string{"run.ai"}, APIVersions: []string{"v1alpha2"}, Resources: []string{"kartas"}}}}}}}
			objects := []client.Object{crd, hook}
			if existing {
				objects = append(objects, definition)
			}
			base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithIndex(&api.Karta{}, kartaGVKIndexKey, indexKartaByRootGVK).Build()
			validator := &KartaValidator{client: base}
			creates, updates := 0, 0
			c := interceptor.NewClient(base, interceptor.Funcs{
				Create: func(ctx context.Context, _ client.WithWatch, object client.Object, opts ...client.CreateOption) error {
					creates++
					options := &client.CreateOptions{}
					for _, opt := range opts {
						opt.ApplyToCreate(options)
					}
					if len(options.DryRun) != 1 {
						t.Fatal("readiness attempted real Create")
					}
					candidate := object.(*api.Karta)
					if err := (&KartaLabeler{}).Default(ctx, candidate); err != nil {
						return err
					}
					_, err := validator.ValidateCreate(ctx, candidate)
					return err
				},
				Update: func(ctx context.Context, _ client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
					updates++
					options := &client.UpdateOptions{}
					for _, opt := range opts {
						opt.ApplyToUpdate(options)
					}
					if len(options.DryRun) != 1 {
						t.Fatal("readiness attempted real Update")
					}
					previous := &api.Karta{}
					if err := base.Get(ctx, types.NamespacedName{Name: object.GetName()}, previous); err != nil {
						return err
					}
					_, err := validator.ValidateUpdate(ctx, previous, object.(*api.Karta))
					return err
				},
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := cataloginstall.WaitReady(ctx, c, "validation", definition); err != nil {
				t.Fatalf("admission readiness: %v", err)
			}
			if existing && (creates != 0 || updates != 1) {
				t.Fatalf("upgrade used create=%d update=%d", creates, updates)
			}
			if !existing && (creates != 1 || updates != 0) {
				t.Fatalf("install used create=%d update=%d", creates, updates)
			}
		})
	}
}
