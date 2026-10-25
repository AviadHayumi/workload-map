// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type source struct {
	list   *v1alpha1.KartaList
	stream *watch.RaceFreeFakeWatcher
	err    error
}

func (s *source) List(context.Context, metav1.ListOptions) (*v1alpha1.KartaList, error) {
	return s.list.DeepCopy(), s.err
}
func (s *source) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return s.stream, s.err
}
func TestInventory(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Karta inventory") }

var _ = Describe("inventory snapshots", func() {
	gvk := schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	var s *source
	var i *Inventory
	var cancel context.CancelFunc
	var done chan error
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
		i, err = New(config, s)
		Expect(err).NotTo(HaveOccurred())
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		done = make(chan error, 1)
		go func() { done <- i.Run(ctx) }()
		Eventually(func() error { _, err := i.Lookup(gvk); return err }).Should(Succeed())
	}
	BeforeEach(func() {
		s = &source{list: &v1alpha1.KartaList{ListMeta: metav1.ListMeta{ResourceVersion: "10"}}, stream: watch.NewRaceFreeFake()}
		cancel = nil
	})
	AfterEach(func() {
		if cancel != nil {
			cancel()
			Eventually(done).Should(Receive())
		}
	})
	It("elects oldest then name and preserves cluster provenance", func() {
		a, b, c := definition("b", 1), definition("a", 1), definition("c", 2)
		s.list.Items = []v1alpha1.Karta{*a, *b, *c}
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
		s.list.Items = []v1alpha1.Karta{*a, *b, *c}
		start(Config{Cluster: ClusterConfig{Enabled: true, Include: []string{"a", "b", "c"}, Exclude: []string{"b"}, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"enabled": "yes"}}}})
		selection, _ := i.Lookup(gvk)
		Expect(selection.Origin.Name).To(Equal("c"))
	})
	It("does not fall back until cluster synchronization", func() {
		var err error
		i, err = New(Config{Cluster: ClusterConfig{Enabled: true}, Catalog: CatalogConfig{Enabled: true}}, s)
		Expect(err).NotTo(HaveOccurred())
		_, err = i.Lookup(gvk)
		Expect(errors.Is(err, ErrPending)).To(BeTrue())
		_, err = i.Selections()
		Expect(errors.Is(err, ErrPending)).To(BeTrue())
	})
	It("falls back on confirmed absence with catalog-only provenance", func() {
		start(Config{Cluster: ClusterConfig{Enabled: true}, Catalog: CatalogConfig{Enabled: true}})
		selection, err := i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.Origin.Source).To(Equal(SourceCatalog))
		Expect(selection.Origin.UID).To(BeEmpty())
		Expect(selection.Origin.ResourceVersion).To(BeEmpty())
	})
	It("handles updates and rejects stale UID deletes", func() {
		old := definition("a", 1)
		s.list.Items = []v1alpha1.Karta{*old}
		start(Config{Cluster: ClusterConfig{Enabled: true}})
		replacement := old.DeepCopy()
		replacement.UID = "replacement"
		replacement.ResourceVersion = "13"
		s.stream.Modify(replacement)
		Eventually(func() types.UID { v, _ := i.Lookup(gvk); return v.Origin.UID }).Should(Equal(types.UID("replacement")))
		s.stream.Delete(old)
		Consistently(func() types.UID { v, _ := i.Lookup(gvk); return v.Origin.UID }, 50*time.Millisecond).Should(Equal(types.UID("replacement")))
		s.stream.Delete(replacement)
		Eventually(func() *v1alpha1.Karta { v, _ := i.Lookup(gvk); return v.Definition }).Should(BeNil())
	})
	It("marks a broken stream unavailable instead of falling back", func() {
		start(Config{Cluster: ClusterConfig{Enabled: true}, Catalog: CatalogConfig{Enabled: true}})
		s.stream.Stop()
		Eventually(func() bool { _, err := i.Lookup(gvk); return errors.Is(err, ErrUnavailable) }).Should(BeTrue())
	})
	It("skips disabled sources", func() {
		var err error
		i, err = New(Config{}, nil)
		Expect(err).NotTo(HaveOccurred())
		v, err := i.Lookup(gvk)
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Definition).To(BeNil())
		Expect(i.Run(context.Background())).To(Succeed())
	})
})
