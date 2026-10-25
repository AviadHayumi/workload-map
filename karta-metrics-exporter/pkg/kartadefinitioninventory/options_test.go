// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
)

var _ = Describe("Options", func() {
	DescribeTable("rejects options that cannot be applied",
		func(options Options, wantProblems ...string) {
			_, _, err := options.compile()

			Expect(err).To(MatchError(ErrInvalidOptions))
			for _, problem := range wantProblems {
				Expect(err.Error()).To(ContainSubstring(problem))
			}
		},
		Entry("an unknown catalog include name",
			Options{Catalog: SourceOptions{Enabled: true, Include: []string{"batch-job-v2"}}},
			`catalog.include: unknown definition "batch-job-v2"`),
		Entry("an unknown catalog exclude name, even with the catalog disabled",
			Options{Catalog: SourceOptions{Exclude: []string{"core-pod"}}},
			`catalog.exclude: unknown definition "core-pod"`),
		Entry("an empty name",
			Options{Cluster: SourceOptions{Enabled: true, Include: []string{""}}},
			"cluster.include: empty definition name"),
		Entry("a duplicate name",
			Options{Cluster: SourceOptions{Enabled: true, Exclude: []string{"a", "a"}}},
			`cluster.exclude: duplicate definition name "a"`),
		Entry("a malformed selector",
			Options{Cluster: SourceOptions{Enabled: true, LabelSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "team", Operator: "Contains", Values: []string{"x"}}},
			}}},
			"cluster.labelSelector"),
		Entry("a catalog label selector",
			Options{Catalog: SourceOptions{Enabled: true, LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "x"}}}},
			"catalog.labelSelector: catalog definitions carry no labels"),
		Entry("every problem at once",
			Options{
				Catalog: SourceOptions{Include: []string{"nope"}},
				Cluster: SourceOptions{Enabled: true, Exclude: []string{""}},
			},
			`catalog.include: unknown definition "nope"`, "cluster.exclude: empty definition name"),
	)

	It("lists the known catalog names when a name is unknown", func() {
		_, _, err := Options{Catalog: SourceOptions{Include: []string{"nope"}}}.compile()

		Expect(err).To(MatchError(ContainSubstring("known: ")))
		Expect(err.Error()).To(ContainSubstring("batch-job-v1"))
		Expect(err.Error()).To(ContainSubstring("core-pod-v1"))
	})

	DescribeTable("accepts",
		func(options Options) {
			_, _, err := options.compile()
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("the defaults", DefaultOptions()),
		Entry("both sources disabled", Options{}),
		Entry("a cluster include name no definition has yet",
			Options{Cluster: SourceOptions{Enabled: true, Include: []string{"not-created-yet"}}}),
		Entry("the same name in include and exclude, where exclude wins",
			Options{Cluster: SourceOptions{Enabled: true, Include: []string{"a"}, Exclude: []string{"a"}}}),
		Entry("an empty catalog label selector",
			Options{Catalog: SourceOptions{Enabled: true, LabelSelector: &metav1.LabelSelector{}}}),
	)

	It("reads the cluster by default, leaves the catalog off, and keeps core-pod-v1 excluded", func() {
		Expect(DefaultOptions()).To(Equal(Options{
			Catalog: SourceOptions{Exclude: []string{"core-pod-v1"}},
			Cluster: SourceOptions{Enabled: true},
		}))
	})

	Context("compiled cluster selector", func() {
		definitions := []*v1alpha1.Karta{
			newKarta("a", deploymentGVK, withLabels(map[string]string{"team": "x"})),
			newKarta("b", jobGVK),
		}

		DescribeTable("selects",
			func(selector *metav1.LabelSelector, include []string, want []string) {
				_, cluster, err := Options{Cluster: SourceOptions{Enabled: true, Include: include, LabelSelector: selector}}.compile()
				Expect(err).NotTo(HaveOccurred())

				Expect(selectedNames(resolve(nil, definitions, disabled, cluster))).To(Equal(want))
			},
			Entry("everything for a nil selector, not nothing", nil, nil, []string{"a", "b"}),
			Entry("everything for an empty selector", &metav1.LabelSelector{}, nil, []string{"a", "b"}),
			Entry("only the included names when the selector is empty",
				&metav1.LabelSelector{}, []string{"b"}, []string{"b"}),
			Entry("by matchLabels", &metav1.LabelSelector{MatchLabels: map[string]string{"team": "x"}}, nil, []string{"a"}),
			Entry("by matchExpressions, where NotIn also matches a missing label",
				&metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "team", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"x"}},
				}},
				nil, []string{"b"}),
		)
	})
})
