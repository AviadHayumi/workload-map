// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"fmt"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("Inventory store updates", func() {
	It("removes an old group-kind entry when a definition changes kind", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		store := reflectorStore{inventory: inventory}
		Expect(store.Replace([]any{unstructuredKarta(newKarta("moving", jobGVK))}, "1")).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK).Name).To(Equal("moving"))

		Expect(store.Update(unstructuredKarta(newKarta("moving", deploymentGVK)))).To(Succeed())

		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))
		Expect(lookupOrigin(inventory, deploymentGVK).Name).To(Equal("moving"))
	})

	It("rechecks label filtering when a definition enters or leaves the selection", func() {
		options := withCatalog
		options.Cluster.LabelSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "x"}}
		inventory, err := New(newFakeClient(), options)
		Expect(err).NotTo(HaveOccurred())
		store := reflectorStore{inventory: inventory}
		Expect(store.Replace([]any{unstructuredKarta(newKarta("selected", jobGVK))}, "1")).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))

		Expect(store.Update(unstructuredKarta(newKarta("selected", jobGVK, withLabels(map[string]string{"team": "x"}))))).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK).Name).To(Equal("selected"))
		Expect(store.Update(unstructuredKarta(newKarta("selected", jobGVK)))).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK)).To(Equal(catalogJobOrigin))
	})

	It("recovers from a decode error when the same definition is repaired", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		store := reflectorStore{inventory: inventory}
		broken := unstructuredKarta(newKarta("repaired", jobGVK))
		broken.Object["spec"] = "not-an-object"
		Expect(store.Replace([]any{broken}, "1")).To(Succeed())
		Expect(inventory.Ready()).To(MatchError(ErrUnavailable))

		Expect(store.Update(unstructuredKarta(newKarta("repaired", jobGVK)))).To(Succeed())

		Expect(inventory.Ready()).To(Succeed())
		Expect(lookupOrigin(inventory, jobGVK).Name).To(Equal("repaired"))
	})

	It("returns independent definitions from live lookups and listings", func() {
		inventory, err := New(newFakeClient(), withCatalog)
		Expect(err).NotTo(HaveOccurred())
		store := reflectorStore{inventory: inventory}
		Expect(store.Replace([]any{unstructuredKarta(newKarta("cluster-job", jobGVK))}, "1")).To(Succeed())
		for _, gvk := range []schema.GroupVersionKind{jobGVK, deploymentGVK} {
			sel, err := inventory.Lookup(gvk)
			Expect(err).NotTo(HaveOccurred())
			original := sel.Definition.DeepCopy()
			sel.Definition.Name = "mutated"
			sel.Definition.Spec.StructureDefinition.RootComponent.Kind.Kind = "Mutated"
			listed, err := inventory.Selections()
			Expect(err).NotTo(HaveOccurred())
			for _, entry := range listed {
				if entry.GVK.GroupKind() == gvk.GroupKind() {
					Expect(entry.Definition).To(Equal(original))
					entry.Definition.Spec.StructureDefinition.RootComponent.Kind.Kind = "MutatedAgain"
				}
			}
			sel, err = inventory.Lookup(gvk)
			Expect(err).NotTo(HaveOccurred())
			Expect(sel.Definition).To(Equal(original))
		}
	})

	It("never mixes generations while full relists replace the cluster", func() {
		inventory, err := New(newFakeClient(), DefaultOptions())
		Expect(err).NotTo(HaveOccurred())
		store := reflectorStore{inventory: inventory}
		generations := make([][]any, 2)
		for generation := range generations {
			for _, gvk := range []schema.GroupVersionKind{jobGVK, deploymentGVK, podGVK} {
				name := fmt.Sprintf("generation-%d-%s", generation, gvk.Kind)
				generations[generation] = append(generations[generation], unstructuredKarta(newKarta(name, gvk, fromCluster("", fmt.Sprint(generation)))))
			}
		}
		Expect(store.Replace(generations[0], "0")).To(Succeed())
		var readers sync.WaitGroup
		begin := make(chan struct{})
		for range 4 {
			readers.Go(func() {
				defer GinkgoRecover()
				<-begin
				for range 100 {
					listed, err := inventory.Selections()
					Expect(err).NotTo(HaveOccurred())
					Expect(listed).To(HaveLen(3))
					for _, entry := range listed {
						Expect(entry.Origin.ResourceVersion).To(Equal(listed[0].Origin.ResourceVersion))
					}
				}
			})
		}
		close(begin)
		for n := range 100 {
			Expect(store.Replace(generations[n%2], fmt.Sprint(n))).To(Succeed())
		}
		readers.Wait()
	})
})
