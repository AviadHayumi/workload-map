//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller_test

import (
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Zero-Pod candidate admission through the API", func() {
	It("discovers an unsupported generic ancestor from a catalog-selected child", func(ctx SpecContext) {
		ns := namespace(ctx)
		ancestor := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "generic-parent", Namespace: ns}}
		Expect(api.Create(ctx, ancestor)).To(Succeed())
		child := deployment(ctx, ns, "selected-child")
		child.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: ancestor.Name, UID: ancestor.UID, Controller: ptr.To(true)}}
		Expect(api.Update(ctx, child)).To(Succeed())
		_, url, _ := start(defaults(ns))

		check := func(g Gomega) {
			text := scrape(url)
			expectGenericCandidateRoot(g, text, ancestor, "ConfigMap")
			g.Expect(text).NotTo(ContainSubstring(`workload_uid="` + string(child.UID) + `"`))
		}
		Eventually(check).Should(Succeed())
		// Keep the quiet path valid beyond MaxAge without any Pod events.
		Consistently(check, 3*time.Second).Should(Succeed())
		pods := &corev1.PodList{}
		Expect(api.List(ctx, pods, client.InNamespace(ns))).To(Succeed())
		Expect(pods.Items).To(BeEmpty())
	})

	It("follows generic ancestor reparenting without a child or Pod event", func(ctx SpecContext) {
		ns := namespace(ctx)
		ancestor := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "moving-parent", Namespace: ns}}
		Expect(api.Create(ctx, ancestor)).To(Succeed())
		child := deployment(ctx, ns, "quiet-child")
		child.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: ancestor.Name, UID: ancestor.UID, Controller: ptr.To(true)}}
		Expect(api.Update(ctx, child)).To(Succeed())
		childUID, childVersion := child.UID, child.ResourceVersion
		_, url, _ := start(defaults(ns))
		Eventually(func(g Gomega) {
			expectGenericCandidateRoot(g, scrape(url), ancestor, "ConfigMap")
		}).Should(Succeed())

		parent := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "generic-root-"}}
		Expect(api.Create(ctx, parent)).To(Succeed())
		Expect(api.Get(ctx, client.ObjectKeyFromObject(ancestor), ancestor)).To(Succeed())
		ancestor.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: parent.Name, UID: parent.UID, Controller: ptr.To(true)}}
		Expect(api.Update(ctx, ancestor)).To(Succeed())

		check := func(g Gomega) {
			text := scrape(url)
			expectGenericCandidateRoot(g, text, parent, "Namespace")
			for _, uid := range []string{string(child.UID), string(ancestor.UID)} {
				g.Expect(text).NotTo(ContainSubstring(`workload_uid="` + uid + `"`))
			}
		}
		Eventually(check).Should(Succeed())
		Consistently(check, 3*time.Second).Should(Succeed())
		Expect(api.Get(ctx, client.ObjectKeyFromObject(child), child)).To(Succeed())
		Expect(child.UID).To(Equal(childUID))
		Expect(child.ResourceVersion).To(Equal(childVersion))
		pods := &corev1.PodList{}
		Expect(api.List(ctx, pods, client.InNamespace(ns))).To(Succeed())
		Expect(pods.Items).To(BeEmpty())
	})
})

func expectGenericCandidateRoot(g Gomega, text string, root client.Object, kind string) {
	created, healthy := 0, false
	for _, line := range strings.Split(text, "\n") {
		g.Expect(line).NotTo(HavePrefix("karta_pod_workload_info{"))
		if strings.Contains(line, `workload_uid="`+string(root.GetUID())+`"`) {
			g.Expect(line).NotTo(HavePrefix("karta_workload_status{"))
			g.Expect(line).NotTo(HavePrefix("karta_exporter_observation_valid{"), "unsupported roots must not produce workload failures")
			if strings.HasPrefix(line, "karta_workload_created_timestamp_seconds{") {
				g.Expect(line).To(ContainSubstring(`workload_name="` + root.GetName() + `"`))
				g.Expect(line).To(ContainSubstring(`namespace="` + root.GetNamespace() + `"`))
				fields := strings.Fields(line)
				g.Expect(fields).To(HaveLen(2))
				value, err := strconv.ParseFloat(fields[1], 64)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(value).To(Equal(float64(root.GetCreationTimestamp().Unix())))
				created++
			}
		}
		if strings.HasPrefix(line, "karta_exporter_observation_valid{") && strings.Contains(line, `scope="resource_type"`) && strings.Contains(line, `resource_kind="`+kind+`"`) && strings.Contains(line, `namespace="`+root.GetNamespace()+`"`) {
			g.Expect(line).To(HaveSuffix(" 1"))
			healthy = true
		}
	}
	g.Expect(created).To(Equal(1), "the generic root must publish its verified creation time")
	g.Expect(healthy).To(BeTrue(), "the generic root resource must have healthy collection")
}
