//go:build monitoring

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package monitoring_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const namespace = "exporter-monitoring"
const contextName = "kind-exporter-monitoring"

var kubeconfig, image, output string

func TestMonitoring(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Exporter deployed monitoring")
}

func command(ctx context.Context, name string, input string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(input)
	cmd.Stderr = &stderr
	body, err := cmd.Output()
	if stderr.Len() > 0 {
		fmt.Fprint(GinkgoWriter, stderr.String())
	}
	if err != nil {
		return string(body) + stderr.String(), err
	}
	return string(body), err
}
func kube(ctx context.Context, input string, args ...string) (string, error) {
	return command(ctx, "kubectl", input, append([]string{"--kubeconfig", kubeconfig, "--context", contextName}, args...)...)
}
func mustKube(ctx context.Context, input string, args ...string) string {
	body, err := kube(ctx, input, args...)
	Expect(err).NotTo(HaveOccurred(), body)
	return body
}
func helm(ctx context.Context, extra ...string) (string, error) {
	split := strings.LastIndex(image, ":")
	args := []string{"upgrade", "--install", "monitoring", "../../../charts/karta", "--kubeconfig", kubeconfig, "--kube-context", contextName, "--namespace", namespace, "--set", "operator.enabled=false", "--set", "crdUpgrader.enabled=false", "--set", "exporter.enabled=true", "--set", "exporter.image.repository=" + image[:split], "--set", "exporter.image.tag=" + image[split+1:], "--set", "exporter.image.pullPolicy=Never", "--set", "exporter.kartaDefinitionSource.cluster.enabled=false", "--set", "exporter.kartaDefinitionSource.catalog.enabled=true", "--set-json", `exporter.kartaDefinitionSource.catalog.include=["apps-deployment-v1"]`, "--set-json", `exporter.namespaceSelector={"matchLabels":{"kubernetes.io/metadata.name":"exporter-monitoring"}}`, "--set-json", `exporter.objectSelector={"matchLabels":{"monitoring-workload":"true"}}`, "--set-json", `exporter.rbac.workloadRules=[{"apiGroups":["apps"],"resources":["deployments","replicasets"]}]`, "--set", "exporter.serviceMonitor.enabled=true", "--set", "exporter.serviceMonitor.labels.monitoring-test=selected", "--set", "exporter.serviceMonitor.interval=2s", "--set", "exporter.serviceMonitor.scrapeTimeout=1s"}
	return command(ctx, "helm", "", append(args, extra...)...)
}
func query(ctx context.Context, expression string) (int, error) {
	raw, err := kube(ctx, "", "get", "--raw", "/api/v1/namespaces/"+namespace+"/services/prometheus:9090/proxy/api/v1/query?query="+url.QueryEscape(expression))
	if err != nil {
		return 0, err
	}
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(raw), &result); err != nil {
		return 0, err
	}
	if result.Status != "success" {
		return 0, fmt.Errorf("query status %s", result.Status)
	}
	return len(result.Data.Result), nil
}
func save(name, body string) {
	Expect(os.WriteFile(filepath.Join(output, name), []byte(body), 0600)).To(Succeed())
}

var _ = BeforeSuite(func(ctx SpecContext) {
	kubeconfig = os.Getenv("KUBECONFIG")
	image = os.Getenv("MONITORING_EXPORTER_IMAGE")
	output = os.Getenv("MONITORING_OUTPUT")
	Expect(kubeconfig).NotTo(BeEmpty())
	Expect(image).To(ContainSubstring(":"))
	Expect(image).NotTo(ContainSubstring("@"))
	if output == "" {
		output = filepath.Join(os.TempDir(), "exporter-monitoring-results")
	}
	Expect(os.MkdirAll(output, 0700)).To(Succeed())
	save("image-reference.txt", image+"\n")
	raw := mustKube(ctx, "", "config", "view", "--minify", "-o", "jsonpath={.current-context}")
	Expect(raw).To(Equal(contextName))
	// This cluster name and node label guard precedes all installation or deletion.
	nodes := mustKube(ctx, "", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}")
	Expect(strings.Fields(nodes)).To(Equal([]string{"exporter-monitoring-control-plane"}))
	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)
}, NodeTimeout(time.Minute))

var _ = Describe("Standalone monitoring lifecycle", func() {
	It("installs, discovers, enforces access, upgrades and disables", func(ctx SpecContext) {
		_, err := kube(ctx, "", "get", "crd", "servicemonitors.monitoring.coreos.com")
		Expect(err).To(HaveOccurred(), "start on the fresh monitoring cluster, before installing Prometheus Operator")
		mustKube(ctx, "", "create", "namespace", namespace)
		By("reporting the missing ServiceMonitor CRD")
		body, err := helm(ctx)
		Expect(err).To(HaveOccurred())
		Expect(body).To(And(ContainSubstring("ServiceMonitor"), ContainSubstring("no matches for kind")))
		save("missing-crd.txt", body)
		_, _ = command(ctx, "helm", "", "uninstall", "monitoring", "--kubeconfig", kubeconfig, "--kube-context", contextName, "--namespace", namespace)
		By("installing the checksum-pinned operator")
		body, err = command(ctx, "bash", "", "setup.sh", "operator")
		Expect(err).NotTo(HaveOccurred(), body)
		mustKube(ctx, "", "apply", "-f", "prometheus.yaml")
		body, err = helm(ctx, "--wait", "--timeout", "3m")
		Expect(err).NotTo(HaveOccurred(), body)
		deployedImage := mustKube(ctx, "", "-n", namespace, "get", "pod", "-l", "app.kubernetes.io/name=karta-metrics-exporter", "-o", `jsonpath={.items[*].spec.containers[?(@.name=="karta-metrics-exporter")].image}`)
		Expect(strings.Fields(deployedImage)).To(Equal([]string{image}))
		imageID := mustKube(ctx, "", "-n", namespace, "get", "pod", "-l", "app.kubernetes.io/name=karta-metrics-exporter", "-o", `jsonpath={.items[*].status.containerStatuses[?(@.name=="karta-metrics-exporter")].imageID}`)
		Expect(strings.Fields(imageID)).To(HaveLen(1))
		save("image-id.txt", imageID+"\n")
		AddReportEntry("exporter image", map[string]string{"reference": image, "imageID": imageID})
		serviceAccount := mustKube(ctx, "", "-n", namespace, "get", "deployment", "karta-metrics-exporter", "-o", "jsonpath={.spec.template.spec.serviceAccountName}")
		Expect(serviceAccount).To(Equal("karta-metrics-exporter"))
		identity := "system:serviceaccount:" + namespace + ":" + serviceAccount
		mustKube(ctx, "", "-n", namespace, "rollout", "status", "statefulset/prometheus-acceptance", "--timeout=300s")
		mustKube(ctx, "", "-n", namespace, "create", "deployment", "observed", "--image=registry.k8s.io/pause:3.10")
		mustKube(ctx, "", "-n", namespace, "label", "deployment", "observed", "monitoring-workload=true")
		mustKube(ctx, "", "-n", namespace, "rollout", "status", "deployment/observed", "--timeout=120s")
		rootUID := mustKube(ctx, "", "-n", namespace, "get", "deployment", "observed", "-o", "jsonpath={.metadata.uid}")
		for _, metric := range []string{"karta_workload_status", "karta_workload_created_timestamp_seconds", "karta_pod_workload_info", "karta_exporter_observation_valid"} {
			expression := metric
			if metric != "karta_exporter_observation_valid" {
				expression += `{workload_uid="` + rootUID + `"}`
			}
			Eventually(func() (int, error) { return query(ctx, expression) }).Should(BeNumerically(">", 0), metric)
		}
		direct := mustKube(ctx, "", "get", "--raw", "/api/v1/namespaces/"+namespace+"/services/karta-metrics-exporter:8080/proxy/metrics")
		for _, family := range []string{"karta_workload_status", "karta_workload_created_timestamp_seconds", "karta_pod_workload_info", "karta_exporter_observation_valid"} {
			Expect(direct).To(ContainSubstring(family + "{"))
		}
		save("direct-metrics.prom", direct)
		By("proving policy enforcement with positive and negative controls")
		exporterIP := mustKube(ctx, "", "-n", namespace, "get", "pod", "-l", "app.kubernetes.io/name=karta-metrics-exporter", "-o", "jsonpath={.items[0].status.podIP}")
		mustKube(ctx, "", "-n", namespace, "run", "unauthorized", "--image=busybox:1.36.1", "--restart=Never", "--command", "--", "sleep", "3600")
		mustKube(ctx, "", "-n", namespace, "wait", "pod/unauthorized", "--for=condition=Ready", "--timeout=120s")
		probe := func() error {
			_, err := kube(ctx, "", "-n", namespace, "exec", "unauthorized", "--", "wget", "-q", "-T", "3", "-O", "/dev/null", "http://"+exporterIP+":8080/metrics")
			return err
		}
		Expect(probe()).To(Succeed(), "control must connect before policy")
		endpoints := mustKube(ctx, "", "-n", "default", "get", "endpoints", "kubernetes", "-o", "jsonpath={.subsets[*].addresses[*].ip}")
		serviceIP := mustKube(ctx, "", "-n", "default", "get", "service", "kubernetes", "-o", "jsonpath={.spec.clusterIP}")
		peers := []any{}
		for _, ip := range append(strings.Fields(endpoints), serviceIP) {
			peers = append(peers, map[string]any{"ipBlock": map[string]any{"cidr": ip + "/32"}})
		}
		policy := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "exporter-test", "namespace": namespace}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": "karta-metrics-exporter"}}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"prometheus": "acceptance"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8080}}}}, "egress": []any{map[string]any{"to": peers, "ports": []any{map[string]any{"protocol": "TCP", "port": 443}, map[string]any{"protocol": "TCP", "port": 6443}}}}}}
		encoded, err := json.Marshal(policy)
		Expect(err).NotTo(HaveOccurred())
		mustKube(ctx, string(encoded), "apply", "-f", "-")
		Eventually(probe).ShouldNot(Succeed())
		Consistently(probe, 10*time.Second).ShouldNot(Succeed())
		// Restart under policy so the new root requires fresh API connections.
		mustKube(ctx, "", "-n", namespace, "rollout", "restart", "deployment/karta-metrics-exporter")
		mustKube(ctx, "", "-n", namespace, "rollout", "status", "deployment/karta-metrics-exporter", "--timeout=180s")
		mustKube(ctx, "", "-n", namespace, "create", "deployment", "after-policy", "--image=registry.k8s.io/pause:3.10")
		mustKube(ctx, "", "-n", namespace, "label", "deployment", "after-policy", "monitoring-workload=true")
		freshUID := mustKube(ctx, "", "-n", namespace, "get", "deployment", "after-policy", "-o", "jsonpath={.metadata.uid}")
		Eventually(func() (int, error) {
			return query(ctx, `karta_workload_status{workload_uid="`+freshUID+`",observation_valid="true"}`)
		}).Should(Equal(9))
		By("reporting missing root LIST/WATCH permission and recovering")
		body, err = helm(ctx, "--set-json", `exporter.rbac.workloadRules=[{"apiGroups":["apps"],"resources":["replicasets"]}]`)
		Expect(err).NotTo(HaveOccurred(), body)
		for _, verb := range []string{"list", "watch"} {
			Eventually(func(g Gomega) {
				answer, err := kube(ctx, "", "-n", namespace, "auth", "can-i", verb, "deployments.apps", "--as", identity, "--as-group", "system:serviceaccounts", "--as-group", "system:serviceaccounts:"+namespace, "--as-group", "system:authenticated")
				g.Expect(strings.TrimSpace(answer)).To(Equal("no"), identity+" "+verb+" deployments.apps in "+namespace)
				g.Expect(err).To(HaveOccurred())
			}).Should(Succeed())
		}
		mustKube(ctx, "", "-n", namespace, "rollout", "restart", "deployment/karta-metrics-exporter")
		Eventually(func() (int, error) {
			return query(ctx, `karta_exporter_observation_valid{resource_kind="Deployment",reason="permission_denied"} == 0`)
		}).Should(BeNumerically(">", 0))
		By("upgrading the running release and recovering observations")
		body, err = helm(ctx, "--set", "exporter.podAnnotations.acceptance-revision=second", "--wait", "--timeout", "3m")
		Expect(err).NotTo(HaveOccurred(), body)
		for _, verb := range []string{"list", "watch"} {
			Eventually(func(g Gomega) {
				answer, err := kube(ctx, "", "-n", namespace, "auth", "can-i", verb, "deployments.apps", "--as", identity, "--as-group", "system:serviceaccounts", "--as-group", "system:serviceaccounts:"+namespace, "--as-group", "system:authenticated")
				g.Expect(err).NotTo(HaveOccurred(), answer)
				g.Expect(strings.TrimSpace(answer)).To(Equal("yes"), identity+" "+verb+" deployments.apps in "+namespace)
			}).Should(Succeed())
		}
		recoveredAfter := time.Now().Unix()
		mustKube(ctx, "", "-n", namespace, "create", "deployment", "after-recovery", "--image=registry.k8s.io/pause:3.10")
		mustKube(ctx, "", "-n", namespace, "label", "deployment", "after-recovery", "monitoring-workload=true")
		recoveredUID := mustKube(ctx, "", "-n", namespace, "get", "deployment", "after-recovery", "-o", "jsonpath={.metadata.uid}")
		Expect(recoveredUID).NotTo(BeEmpty())
		Expect(recoveredUID).NotTo(BeElementOf(rootUID, freshUID))
		Eventually(func() (int, error) {
			return query(ctx, `karta_workload_status{workload_uid="`+recoveredUID+`",observation_valid="true"}`)
		}).Should(Equal(9))
		health := `karta_exporter_observation_valid{scope="resource_type",namespace="` + namespace + `",resource_group="apps",resource_version="v1",resource_kind="Deployment",reason="none"}`
		Eventually(func() (int, error) {
			return query(ctx, fmt.Sprintf("(%s == 1) and (timestamp(%s) > %d)", health, health, recoveredAfter))
		}).Should(BeNumerically(">", 0))
		upgradedIDs := strings.Fields(mustKube(ctx, "", "-n", namespace, "get", "pod", "-l", "app.kubernetes.io/name=karta-metrics-exporter", "-o", `jsonpath={.items[*].status.containerStatuses[?(@.name=="karta-metrics-exporter")].imageID}`))
		Expect(upgradedIDs).NotTo(BeEmpty())
		for _, upgradedID := range upgradedIDs {
			Expect(upgradedID).To(Equal(imageID))
		}
		save("acceptance.txt", "ServiceMonitor collected all four families. NetworkPolicy denied the control Pod after allowing it before policy; fresh root metrics passed under policy. Effective Deployment LIST/WATCH permissions were denied before restart. The exporter reported permission_denied. After upgrade, permissions were restored and a new root UID plus a new healthy Deployment observation proved recovery. The deployed image ID stayed unchanged.\n")
		By("removing runtime resources by disabling the exporter")
		body, err = helm(ctx, "--set", "exporter.enabled=false")
		Expect(err).NotTo(HaveOccurred(), body)
		for _, kind := range []string{"deployment", "pod", "service", "serviceaccount", "configmap", "servicemonitor"} {
			Eventually(func() (string, error) {
				return kube(ctx, "", "-n", namespace, "get", kind, "-l", "app.kubernetes.io/name=karta-metrics-exporter", "-o", "name")
			}).Should(BeEmpty(), kind)
		}
		for _, kind := range []string{"clusterrole", "clusterrolebinding"} {
			Eventually(func() (string, error) {
				return kube(ctx, "", "get", kind, "-l", "app.kubernetes.io/name=karta-metrics-exporter", "-o", "name")
			}).Should(BeEmpty(), kind)
		}
		save("disabled.txt", "Exporter Deployment, Service, ServiceAccount, ConfigMap and ServiceMonitor removed by Helm upgrade.\n")
	}, SpecTimeout(20*time.Minute))
})
