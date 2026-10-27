// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"errors"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

func TestInventory(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Karta inventory") }

var _ = Describe("inventory snapshots", func() {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	var definitions []*v1alpha1.Karta
	var i *Inventory
	definition := func(name string, created int64) *v1alpha1.Karta {
		k, err := catalog.Get(gvk)
		Expect(err).NotTo(HaveOccurred())
		k.Name = name
		k.UID = types.UID(name)
		k.ResourceVersion = "12"
		k.CreationTimestamp = metav1.NewTime(time.Unix(created, 0))
		k.Labels = map[string]string{"enabled": "yes"}
		return k
	}
	start := func(config Config) {
		var err error
		i, err = New(config)
		Expect(err).NotTo(HaveOccurred())
		i.ReplaceCluster(definitions)
	}
	BeforeEach(func() { definitions = nil })
	It("elects oldest then name and preserves cluster provenance", func() {
		a, b, c := definition("b", 1), definition("a", 1), definition("c", 2)
		definitions = []*v1alpha1.Karta{a, b, c}
		start(Config{Cluster: ClusterConfig{Enabled: true}, Catalog: CatalogConfig{Enabled: true}})
		selection, err := i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin).To(Equal(Origin{Source: SourceCluster, Name: "a", UID: "a", ResourceVersion: "12"}))
		selection.Definition.Labels["mutated"] = "yes"
		again, _ := i.Lookup(gvk)
		Expect(again.Definition.Labels).NotTo(HaveKey("mutated"))
	})
	It("combines names and labels and lets exclusions win", func() {
		a, b, c := definition("a", 1), definition("b", 2), definition("c", 3)
		a.Labels["enabled"] = "no"
		definitions = []*v1alpha1.Karta{a, b, c}
		start(Config{Cluster: ClusterConfig{Enabled: true, Include: []string{"a", "b", "c"}, Exclude: []string{"b"}, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"enabled": "yes"}}}})
		selection, _ := i.Lookup(gvk)
		Expect(selection.Origin.Name).To(Equal("c"))
	})
	It("falls back on confirmed absence with catalog-only provenance", func() {
		start(Config{Cluster: ClusterConfig{Enabled: true}, Catalog: CatalogConfig{Enabled: true}})
		selection, err := i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.Source).To(Equal(SourceCatalog))
		Expect(selection.Origin.UID).To(BeEmpty())
		Expect(selection.Origin.ResourceVersion).To(BeEmpty())
	})
	It("replaces the complete snapshot and owns its input", func() {
		old := definition("a", 1)
		definitions = []*v1alpha1.Karta{old}
		start(Config{Cluster: ClusterConfig{Enabled: true}})
		replacement := old.DeepCopy()
		replacement.UID = "replacement"
		replacement.ResourceVersion = "13"
		i.ReplaceCluster([]*v1alpha1.Karta{replacement})
		replacement.Name = "mutated"
		selection, err := i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.UID).To(Equal(types.UID("replacement")))
		Expect(selection.Definition.Name).To(Equal("a"))
		i.ReplaceCluster(nil)
		selection, err = i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Definition).To(BeNil())
	})
	It("rejects unknown catalog includes and exclusions at construction", func() {
		for _, config := range []Config{{Catalog: CatalogConfig{Include: []string{"not-in-catalog"}}}, {Catalog: CatalogConfig{Exclude: []string{"not-in-catalog"}}}} {
			_, err := New(config)
			Expect(err).To(MatchError(ContainSubstring("unknown catalog definition")))
		}
	})
	It("keeps an invalid elected definition from falling back", func() {
		bad, good := definition("invalid", 1), definition("valid", 2)
		bad.Spec.StructureDefinition.RootComponent.Name = ""
		definitions = []*v1alpha1.Karta{bad, good}
		start(Config{Cluster: ClusterConfig{Enabled: true}, Catalog: CatalogConfig{Enabled: true}})
		_, err := i.Lookup(gvk)
		Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
		_, first := i.Lookup(gvk)
		_, second := i.Lookup(gvk)
		Expect(first).To(BeIdenticalTo(second))
		i.ReplaceCluster([]*v1alpha1.Karta{good})
		Eventually(func() string { v, _ := i.Lookup(gvk); return v.Origin.Name }).Should(Equal("valid"))
	})
	It("isolates invalid definitions while keeping selected metadata visible", func() {
		bad := definition("invalid-deployment", 1)
		bad.Spec.StructureDefinition.RootComponent.Name = ""
		missing := definition("missing-root-kind", 2)
		missing.Spec.StructureDefinition.RootComponent.Kind = nil
		jobGVK := schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
		job, err := catalog.Get(jobGVK)
		Expect(err).NotTo(HaveOccurred())
		job.Name = "valid-job"
		definitions = []*v1alpha1.Karta{bad, missing, job}
		start(Config{Cluster: ClusterConfig{Enabled: true}})
		chosen, err := i.Lookup(gvk)
		Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
		Expect(chosen.Origin.Name).To(Equal(bad.Name))
		Expect(chosen.GVK).To(Equal(gvk))
		chosen.Definition.Name = "mutated"
		chosenAgain, _ := i.Lookup(gvk)
		Expect(chosenAgain.Definition.Name).To(Equal(bad.Name))
		chosen, err = i.Lookup(jobGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(chosen.Origin.Name).To(Equal(job.Name))
		selections := i.Selections()
		Expect(selections).To(HaveLen(3))
		chosen, err = i.Lookup(schema.GroupVersionKind{})
		Expect(errors.Is(err, ErrInvalidDefinition)).To(BeTrue())
		Expect(chosen.Origin.Name).To(Equal(missing.Name))
		Expect(chosen.GVK).To(Equal(schema.GroupVersionKind{}))
	})

	It("publishes complete relist snapshots during simultaneous reads", func() {
		old := definition("old", 1)
		definitions = []*v1alpha1.Karta{old}
		start(Config{Cluster: ClusterConfig{Enabled: true}})
		var readers sync.WaitGroup
		stopReaders := make(chan struct{})
		defer func() { close(stopReaders); readers.Wait() }()
		for range 8 {
			readers.Go(func() {
				defer GinkgoRecover()
				for {
					select {
					case <-stopReaders:
						return
					default:
					}
					v, err := i.Lookup(gvk)
					if err == nil {
						Expect(v.Definition).NotTo(BeNil())
						Expect(v.Origin.Name).To(BeElementOf("old", "new"))
					}
					_ = i.Selections()
				}
			})
		}
		i.ReplaceCluster([]*v1alpha1.Karta{definition("new", 2)})
		Expect(func() string { v, _ := i.Lookup(gvk); return v.Origin.Name }()).To(Equal("new"))
	})

	It("skips disabled sources", func() {
		var err error
		i, err = New(Config{})
		Expect(err).NotTo(HaveOccurred())
		v, err := i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Definition).To(BeNil())
	})
})
