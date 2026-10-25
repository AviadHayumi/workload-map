//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Selection transitions through the API", func() {
	DescribeTable("withdraws and restores only matching observations", func(ctx SpecContext, selectPods bool) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "moving-root")
		p := pod(ctx, replicaSet(ctx, root, "moving-middle"), "moving-pod")
		stable := deployment(ctx, ns, "stable-root")
		sibling := pod(ctx, replicaSet(ctx, stable, "stable-middle"), "stable-pod")
		const label = "example.com/selected"
		for _, object := range []client.Object{root, p, stable, sibling} {
			labels := object.GetLabels()
			labels[label] = "yes"
			object.SetLabels(labels)
			Expect(api.Update(ctx, object)).To(Succeed())
		}
		cfg := defaults()
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
		selector := &metav1.LabelSelector{MatchLabels: map[string]string{label: "yes"}}
		var changed client.Object = root
		if selectPods {
			cfg.PodSelector = selector
			changed = p
		} else {
			cfg.ObjectSelector = selector
		}
		_, url, _ := start(cfg)
		check := func(g Gomega, selected bool) {
			text := scrape(url)
			rootUID := `workload_uid="` + string(root.UID) + `"`
			counts := map[string]int{}
			healthy := map[string]bool{}
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, rootUID) {
					for _, metric := range []string{"karta_pod_workload_info", "karta_workload_status", "karta_workload_created_timestamp_seconds"} {
						if strings.HasPrefix(line, metric+"{") {
							counts[metric]++
						}
					}
				}
				if strings.HasPrefix(line, "karta_exporter_observation_valid{") && strings.Contains(line, `namespace="`+ns+`"`) {
					g.Expect(line).NotTo(ContainSubstring(`scope="workload"`), "selection is not an observation failure")
					g.Expect(line).To(HaveSuffix(" 1"))
					for _, kind := range []string{"Pod", "Deployment"} {
						if strings.Contains(line, `resource_kind="`+kind+`"`) && strings.Contains(line, `scope="resource_type"`) {
							healthy[kind] = true
						}
					}
				}
			}
			if selected {
				g.Expect(counts["karta_pod_workload_info"]).To(Equal(1))
				g.Expect(infoFor(text, p.Name, ns)).To(And(ContainSubstring(rootUID), ContainSubstring(`observation_valid="true"`)))
			} else {
				g.Expect(counts["karta_pod_workload_info"]).To(BeZero())
				g.Expect(infoFor(text, p.Name, ns)).To(BeEmpty())
			}
			if selected || selectPods {
				g.Expect(counts["karta_workload_status"]).To(Equal(9))
				g.Expect(counts["karta_workload_created_timestamp_seconds"]).To(Equal(1))
			} else {
				g.Expect(counts["karta_workload_status"]).To(BeZero())
				g.Expect(counts["karta_workload_created_timestamp_seconds"]).To(BeZero())
			}
			g.Expect(infoFor(text, sibling.Name, ns)).To(And(ContainSubstring(`workload_uid="`+string(stable.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
			g.Expect(healthy).To(HaveKey("Pod"))
			g.Expect(healthy).To(HaveKey("Deployment"))
		}
		Eventually(func(g Gomega) { check(g, true) }).Should(Succeed())
		for _, selected := range []bool{false, true} {
			Expect(api.Get(ctx, client.ObjectKeyFromObject(changed), changed)).To(Succeed())
			labels := changed.GetLabels()
			if selected {
				labels[label] = "yes"
			} else {
				delete(labels, label)
			}
			changed.SetLabels(labels)
			Expect(api.Update(ctx, changed)).To(Succeed())
			Eventually(func(g Gomega) { check(g, selected) }).Should(Succeed())
			Consistently(func(g Gomega) { check(g, selected) }, time.Second).Should(Succeed())
		}
	}, Entry("PodSelector", true), Entry("ObjectSelector", false))
})
