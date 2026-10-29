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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Identity lifecycle through the API and HTTP", func() {
	It("stops at the ReplicaSet below an explicitly ignored Deployment", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "ignored-deployment")
		rs := replicaSet(ctx, root, "observed-replicaset")
		p := pod(ctx, rs, "observed-pod")
		cfg := defaults(ns)
		cfg.KartaDefinitionSource.Catalog.Enabled = false
		cfg.IgnoredOwnerKinds = []schema.GroupVersionKind{deployGVK}
		_, url, _ := start(cfg)
		Eventually(func(g Gomega) {
			text := scrape(url)
			g.Expect(infoFor(text, p.Name, ns)).To(And(
				ContainSubstring(`pod_uid="`+string(p.UID)+`"`),
				ContainSubstring(`workload_uid="`+string(rs.UID)+`"`),
				ContainSubstring(`workload_name="`+rs.Name+`"`),
				ContainSubstring(`workload_group="apps"`),
				ContainSubstring(`workload_version="v1"`),
				ContainSubstring(`workload_kind="ReplicaSet"`),
				ContainSubstring(`owner_resolution="ignored_owner"`),
				ContainSubstring(`observation_valid="true"`),
				ContainSubstring(`component="<unknown>"`),
				ContainSubstring(`component_instance="<unknown>"`),
				ContainSubstring(`replica_group_id=""`),
			))
			g.Expect(text).NotTo(ContainSubstring(`workload_uid="` + string(root.UID) + `"`))
			created := 0
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "karta_workload_created_timestamp_seconds{") && strings.Contains(line, `workload_uid="`+string(rs.UID)+`"`) {
					fields := strings.Fields(line)
					g.Expect(fields).To(HaveLen(2))
					value, err := strconv.ParseFloat(fields[1], 64)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(value).To(Equal(float64(rs.CreationTimestamp.Unix())))
					created++
				}
			}
			g.Expect(created).To(Equal(1))
		}).Should(Succeed())
	})

	It("replaces same-name Pods and roots without retaining old series or changing a sibling", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "recreated-root")
		rs := replicaSet(ctx, root, "recreated-middle")
		p := pod(ctx, rs, "recreated-pod")
		stable := deployment(ctx, ns, "stable-root")
		sibling := pod(ctx, replicaSet(ctx, stable, "stable-middle"), "stable-pod")
		_, url, _ := start(defaults(ns))
		checkSibling := func(g Gomega, text string) {
			g.Expect(infoFor(text, sibling.Name, ns)).To(And(ContainSubstring(`pod_uid="`+string(sibling.UID)+`"`), ContainSubstring(`workload_uid="`+string(stable.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
		}
		Eventually(func(g Gomega) {
			text := scrape(url)
			g.Expect(infoFor(text, p.Name, ns)).To(And(ContainSubstring(`pod_uid="`+string(p.UID)+`"`), ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
			checkSibling(g, text)
		}).Should(Succeed())
		oldPodUID := p.UID
		Expect(api.Delete(ctx, p, client.GracePeriodSeconds(0))).To(Succeed())
		Eventually(func() bool { return apierrors.IsNotFound(api.Get(ctx, client.ObjectKeyFromObject(p), &corev1.Pod{})) }).Should(BeTrue())
		Eventually(func(g Gomega) {
			text := scrape(url)
			g.Expect(text).NotTo(ContainSubstring(`pod_uid="` + string(oldPodUID) + `"`))
			checkSibling(g, text)
		}).Should(Succeed())
		p = pod(ctx, rs, p.Name)
		Expect(p.UID).NotTo(Equal(oldPodUID))
		Eventually(func(g Gomega) {
			text := scrape(url)
			g.Expect(infoFor(text, p.Name, ns)).To(And(ContainSubstring(`pod_uid="`+string(p.UID)+`"`), ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
			g.Expect(text).NotTo(ContainSubstring(`pod_uid="` + string(oldPodUID) + `"`))
			checkSibling(g, text)
		}).Should(Succeed())

		oldRootUID, secondPodUID := root.UID, p.UID
		// envtest has no garbage collector, so remove the explicit child fixtures.
		for _, object := range []client.Object{p, rs, root} {
			Expect(api.Delete(ctx, object, client.GracePeriodSeconds(0))).To(Succeed())
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(api.Get(ctx, client.ObjectKeyFromObject(root), &appsv1.Deployment{}))
		}).Should(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(api.Get(ctx, client.ObjectKeyFromObject(rs), &appsv1.ReplicaSet{}))
		}).Should(BeTrue())
		Eventually(func() bool { return apierrors.IsNotFound(api.Get(ctx, client.ObjectKeyFromObject(p), &corev1.Pod{})) }).Should(BeTrue())
		Eventually(func(g Gomega) {
			text := scrape(url)
			g.Expect(text).NotTo(ContainSubstring(`workload_uid="` + string(oldRootUID) + `"`))
			g.Expect(text).NotTo(ContainSubstring(`pod_uid="` + string(secondPodUID) + `"`))
			checkSibling(g, text)
		}).Should(Succeed())
		root = deployment(ctx, ns, root.Name)
		rs = replicaSet(ctx, root, rs.Name)
		p = pod(ctx, rs, p.Name)
		Expect(root.UID).NotTo(Equal(oldRootUID))
		Expect(p.UID).NotTo(BeElementOf(oldPodUID, secondPodUID))
		checkRecreated := func(g Gomega) {
			text := scrape(url)
			g.Expect(infoFor(text, p.Name, ns)).To(And(ContainSubstring(`pod_uid="`+string(p.UID)+`"`), ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
			for _, uid := range []string{string(oldRootUID), string(oldPodUID), string(secondPodUID)} {
				g.Expect(text).NotTo(ContainSubstring(uid))
			}
			status, created := 0, 0
			for _, line := range strings.Split(text, "\n") {
				if !strings.Contains(line, `workload_uid="`+string(root.UID)+`"`) {
					continue
				}
				if strings.HasPrefix(line, "karta_workload_status{") {
					g.Expect(line).To(ContainSubstring(`observation_valid="true"`))
					status++
				}
				if strings.HasPrefix(line, "karta_workload_created_timestamp_seconds{") {
					created++
				}
			}
			g.Expect(status).To(Equal(9))
			g.Expect(created).To(Equal(1))
			checkSibling(g, text)
		}
		Eventually(checkRecreated).Should(Succeed())
		Consistently(checkRecreated, time.Second).Should(Succeed())
	})
})
