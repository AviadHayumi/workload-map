// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

var _ = Describe("Snapshot", func() {
	catalogOnly := Options{Catalog: SourceOptions{Enabled: true, Exclude: []string{"core-pod-v1"}}}

	It("selects from the real catalog with the catalog filters applied", func() {
		snap, err := NewSnapshot(catalogOnly, nil)
		Expect(err).NotTo(HaveOccurred())

		job, err := snap.Lookup(jobGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Origin).To(Equal(Origin{Source: SourceCatalog, Name: "batch-job-v1"}))
		Expect(job.GVK).To(Equal(jobGVK))

		pod, err := snap.Lookup(podGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(pod).To(Equal(Selection{}))
	})

	It("prefers the catalog's v1beta1 Dynamo definition over v1alpha1, whatever version is asked for", func() {
		snap, err := NewSnapshot(catalogOnly, nil)
		Expect(err).NotTo(HaveOccurred())

		sel, err := snap.Lookup(dynamoAlphaGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(sel.GVK).To(Equal(dynamoBetaGVK))
		Expect(sel.Origin.Name).To(Equal("nvidia-com-dynamographdeployment-v1beta1"))
	})

	It("lets a selected cluster definition override the catalog", func() {
		options := catalogOnly
		options.Cluster = SourceOptions{Enabled: true, LabelSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "example.com/owner", Operator: metav1.LabelSelectorOpIn, Values: []string{"platform"}},
			},
		}}
		snap, err := NewSnapshot(options, []*v1alpha1.Karta{
			newKarta("platform-job", jobGVK, fromCluster("uid-1", "3"), withLabels(map[string]string{"example.com/owner": "platform"})),
			newKarta("team-deployment", deploymentGVK, withLabels(map[string]string{"example.com/owner": "team-a"})),
		})
		Expect(err).NotTo(HaveOccurred())

		job, err := snap.Lookup(jobGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Origin).To(Equal(Origin{Source: SourceCluster, Name: "platform-job", UID: "uid-1", ResourceVersion: "3"}))

		deployment, err := snap.Lookup(deploymentGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(deployment.Origin).To(Equal(Origin{Source: SourceCatalog, Name: "apps-deployment-v1"}))
	})

	It("ignores cluster definitions while the cluster source is disabled", func() {
		snap, err := NewSnapshot(catalogOnly, []*v1alpha1.Karta{newKarta("cluster-job", jobGVK)})
		Expect(err).NotTo(HaveOccurred())

		job, err := snap.Lookup(jobGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Origin.Source).To(Equal(SourceCatalog))
	})

	It("copies the cluster definitions it is given", func() {
		definition := newKarta("cluster-job", jobGVK, withLabels(map[string]string{"team": "x"}))
		snap, err := NewSnapshot(Options{Cluster: SourceOptions{Enabled: true}}, []*v1alpha1.Karta{definition})
		Expect(err).NotTo(HaveOccurred())

		definition.Name = "mutated"
		definition.Labels["team"] = "mutated"

		job, err := snap.Lookup(jobGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Definition.Name).To(Equal("cluster-job"))
		Expect(job.Definition.Labels).To(Equal(map[string]string{"team": "x"}))
	})

	It("reports an invalid winner through Lookup and Selections, and returns it from Candidate", func() {
		snap, err := NewSnapshot(Options{Cluster: SourceOptions{Enabled: true}}, []*v1alpha1.Karta{newKarta("cluster-job", jobGVK, invalid)})
		Expect(err).NotTo(HaveOccurred())

		_, err = snap.Lookup(jobGVK)
		Expect(err).To(MatchError(ErrInvalidDefinition))

		listed, err := snap.Selections()
		Expect(err).To(MatchError(ErrInvalidDefinition))
		Expect(listed).To(HaveLen(1))

		candidate, ok := snap.Candidate(jobGVK)
		Expect(ok).To(BeTrue())
		Expect(candidate.Origin.Name).To(Equal("cluster-job"))
	})

	It("rejects invalid options", func() {
		_, err := NewSnapshot(Options{Catalog: SourceOptions{Include: []string{"nope"}}}, nil)

		Expect(err).To(MatchError(ErrInvalidOptions))
	})

	It("rejects a nil cluster definition", func() {
		_, err := NewSnapshot(DefaultOptions(), []*v1alpha1.Karta{newKarta("a", jobGVK), nil})

		Expect(err).To(MatchError("cluster definition 1 is nil"))
	})

	It("answers as the internal resolver does over the same inputs", func() {
		options := Options{
			Catalog: SourceOptions{Enabled: true, Exclude: []string{"core-pod-v1"}},
			Cluster: SourceOptions{Enabled: true, Exclude: []string{"excluded"}},
		}
		clusterDefinitions := []*v1alpha1.Karta{
			newKarta("job-newer", jobGVK, createdAt(epoch.Add(time.Hour))),
			newKarta("job-older", jobGVK, createdAt(epoch)),
			newKarta("excluded", deploymentGVK),
			newKarta("pod", podGVK),
		}
		catalogFilter, clusterFilter, err := options.compile()
		Expect(err).NotTo(HaveOccurred())
		want, err := resolve(catalog.List(), clusterDefinitions, catalogFilter, clusterFilter).list()
		Expect(err).NotTo(HaveOccurred())

		snap, err := NewSnapshot(options, clusterDefinitions)
		Expect(err).NotTo(HaveOccurred())

		got, err := snap.Selections()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	})
})
