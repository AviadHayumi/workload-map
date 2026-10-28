//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/app"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/config"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/labelresolver"
	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

type requests struct {
	mu              sync.Mutex
	watches         map[string]int
	metadataWatches map[string]int
	streams         map[string][]io.ReadCloser
	deny            string
}
type transport struct {
	next http.RoundTripper
	seen *requests
}

func (t transport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.seen.mu.Lock()
	if request.URL.Query().Get("watch") == "true" && request.URL.Query().Get("sendInitialEvents") != "true" {
		t.seen.watches[request.URL.Path]++
		if strings.Contains(request.Header.Get("Accept"), "PartialObjectMetadata") {
			t.seen.metadataWatches[request.URL.Path]++
		}
	}
	denied := t.seen.deny != "" && strings.Contains(request.URL.Path, t.seen.deny)
	t.seen.mu.Unlock()
	if denied {
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"test denial","code":403}`)), Request: request}, nil
	}
	response, err := t.next.RoundTrip(request)
	if err == nil && request.URL.Query().Get("watch") == "true" && response.StatusCode == http.StatusOK {
		t.seen.mu.Lock()
		t.seen.streams[request.URL.Path] = append(t.seen.streams[request.URL.Path], response.Body)
		denied = t.seen.deny != "" && strings.Contains(request.URL.Path, t.seen.deny)
		t.seen.mu.Unlock()
		if denied {
			_ = response.Body.Close()
		}
	}
	return response, err
}

// Interrupt the live stream so the reflector observes the changed permission on
// reconnect. A transport rule alone cannot revoke an already accepted WATCH.
func (r *requests) denyRequests(path string) {
	r.mu.Lock()
	r.deny = path
	var streams []io.ReadCloser
	if path != "" {
		for name, bodies := range r.streams {
			if strings.Contains(name, path) {
				streams = append(streams, bodies...)
			}
		}
	}
	r.mu.Unlock()
	for _, stream := range streams {
		_ = stream.Close()
	}
}

var environment *envtest.Environment
var kubeConfig *rest.Config
var api client.Client
var deployGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}

func TestController(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Exporter API integration") }

var _ = BeforeSuite(func() {
	Expect(os.Getenv("KUBEBUILDER_ASSETS")).NotTo(BeEmpty(), "integration tests require KUBEBUILDER_ASSETS")
	environment = &envtest.Environment{CRDDirectoryPaths: []string{"../../../../charts/karta/crds"}, ErrorIfCRDPathMissing: true}
	var err error
	kubeConfig, err = environment.Start()
	Expect(err).NotTo(HaveOccurred())
	scheme := runtime.NewScheme()
	Expect(corev1.AddToScheme(scheme)).To(Succeed())
	Expect(appsv1.AddToScheme(scheme)).To(Succeed())
	Expect(batchv1.AddToScheme(scheme)).To(Succeed())
	Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
	api, err = client.New(kubeConfig, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	SetDefaultEventuallyTimeout(15 * time.Second)
	SetDefaultEventuallyPollingInterval(50 * time.Millisecond)
})
var _ = AfterSuite(func() {
	if environment != nil {
		Expect(environment.Stop()).To(Succeed())
	}
})

func namespace(ctx context.Context) string {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "exporter-", Labels: map[string]string{"exporter-test": "selected"}}}
	Expect(api.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

// envtest runs no workload controllers. These helpers create the ownership chain
// and seed Deployment status explicitly to test API observation and collection.
func deployment(ctx context.Context, ns, name string) *appsv1.Deployment {
	root := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"example.com/team": "alpha"}}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "example.test/image:v1"}}}}}}
	Expect(api.Create(ctx, root)).To(Succeed())
	root.Status = appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}}}
	Expect(api.Status().Update(ctx, root)).To(Succeed())
	return root
}
func replicaSet(ctx context.Context, root *appsv1.Deployment, name string) *appsv1.ReplicaSet {
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: root.Namespace, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: root.Name, UID: root.UID, Controller: ptr.To(true)}}}, Spec: appsv1.ReplicaSetSpec{Replicas: ptr.To(int32(1)), Selector: root.Spec.Selector, Template: root.Spec.Template}}
	Expect(api.Create(ctx, rs)).To(Succeed())
	return rs
}
func pod(ctx context.Context, rs *appsv1.ReplicaSet, name string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: rs.Namespace, Labels: map[string]string{"example.com/team": "pod-team"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: rs.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "example.test/image:v1"}}}}
	Expect(api.Create(ctx, p)).To(Succeed())
	return p
}
func start(cfg config.Config, initialDeny ...string) (*app.Application, string, *requests) {
	seen := &requests{watches: map[string]int{}, metadataWatches: map[string]int{}, streams: map[string][]io.ReadCloser{}}
	if len(initialDeny) > 0 {
		seen.deny = initialDeny[0]
	}
	connection := rest.CopyConfig(kubeConfig)
	connection.WrapTransport = func(next http.RoundTripper) http.RoundTripper { return transport{next, seen} }
	application, err := app.New(cfg, connection)
	Expect(err).NotTo(HaveOccurred())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.ServeListener(ctx, listener) }()
	DeferCleanup(func() { cancel(); Eventually(done, 10*time.Second).Should(Receive(Succeed())) })
	return application, "http://" + listener.Addr().String(), seen
}
func defaults(ns string) config.Config {
	cfg := config.DefaultConfig()
	cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
	cfg.KartaDefinitionSource.Catalog.Enabled = true
	cfg.KartaDefinitionSource.Catalog.Include = []string{"apps-deployment-v1"}
	cfg.KartaDefinitionSource.Cluster.Enabled = false
	cfg.RequestTimeout = "1s"
	return cfg
}
func scrape(url string) string {
	response, err := http.Get(url + "/metrics")
	if err != nil {
		return err.Error()
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err.Error()
	}
	return string(body)
}
func infoFor(text, podName string, namespaces ...string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "karta_pod_workload_info{") && strings.Contains(line, `pod_name="`+podName+`"`) && (len(namespaces) == 0 || strings.Contains(line, `namespace="`+namespaces[0]+`"`)) {
			return line
		}
	}
	return ""
}

var _ = Describe("Runtime and real HTTP collection", func() {
	It("shares root watches, reads intermediate metadata, and publishes CEL labels and nine phases", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "root")
		rs := replicaSet(ctx, root, "middle")
		p := pod(ctx, rs, "first")
		pod(ctx, rs, "second")
		cfg := defaults(ns)
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
		cfg.LabelRules = []labelresolver.LabelRule{
			{Name: "team", Metrics: []string{"karta_pod_workload_info", "karta_workload_status"}, Sources: []labelresolver.Source{labelresolver.SourceWorkload}, Expression: `object.metadata.labels["example.com/team"]`},
			{Name: "pod_team", Metrics: []string{"karta_pod_workload_info"}, Sources: []labelresolver.Source{labelresolver.SourcePod}, Expression: `object.metadata.labels["example.com/team"]`},
		}
		_, url, seen := start(cfg)
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(And(ContainSubstring(`team="alpha"`), ContainSubstring(`pod_team="pod-team"`), ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
		text := scrape(url)
		count := 0
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "karta_workload_status{") && strings.Contains(line, `workload_uid="`+string(root.UID)+`"`) {
				count++
			}
		}
		Expect(count).To(Equal(9))
		Expect(api.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		p.Labels["example.com/team"] = "updated-pod-team"
		Expect(api.Update(ctx, p)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(And(ContainSubstring(`pod_team="updated-pod-team"`), ContainSubstring(`team="alpha"`), ContainSubstring(`observation_valid="true"`)))
		Expect(infoFor(scrape(url), "second", ns)).To(ContainSubstring(`pod_team="pod-team"`))
		seen.mu.Lock()
		defer seen.mu.Unlock()
		Expect(seen.watches["/apis/apps/v1/namespaces/"+ns+"/deployments"]).To(Equal(1))
		Expect(seen.metadataWatches["/apis/apps/v1/namespaces/"+ns+"/replicasets"]).To(Equal(1))
	})
	It("publishes zero-Pod roots", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "empty")
		cfg := defaults(ns)
		_, url, _ := start(cfg)
		Eventually(func() string { return scrape(url) }).Should(ContainSubstring(`workload_uid="` + string(root.UID) + `"`))
		Expect(infoFor(scrape(url), "empty")).To(BeEmpty())
	})
	It("evaluates full Pod fields and refreshes Pod labels after a root-only change", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "full-input")
		p := pod(ctx, replicaSet(ctx, root, "full-middle"), "full-pod")
		cfg := defaults(ns)
		cfg.LabelRules = []labelresolver.LabelRule{
			{Name: "scheduler", Metrics: []string{"karta_pod_workload_info"}, Sources: []labelresolver.Source{labelresolver.SourcePod}, Expression: `object.spec.schedulerName`},
			{Name: "pod_phase", Metrics: []string{"karta_pod_workload_info"}, Sources: []labelresolver.Source{labelresolver.SourcePod}, Expression: `object.status.phase`},
			{Name: "desired", Metrics: []string{"karta_pod_workload_info"}, Sources: []labelresolver.Source{labelresolver.SourceWorkload}, Expression: `string(object.spec.replicas)`},
		}
		Expect(api.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		p.Status.Phase = corev1.PodPending
		Expect(api.Status().Update(ctx, p)).To(Succeed())
		_, url, _ := start(cfg)
		Eventually(func() string { return infoFor(scrape(url), p.Name, ns) }).Should(And(
			ContainSubstring(`scheduler="default-scheduler"`), ContainSubstring(`pod_phase="Pending"`),
			ContainSubstring(`desired="1"`), ContainSubstring(`observation_valid="true"`)))
		before := p.ResourceVersion
		Expect(api.Get(ctx, client.ObjectKeyFromObject(root), root)).To(Succeed())
		root.Spec.Replicas = ptr.To(int32(2))
		Expect(api.Update(ctx, root)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, ns) }).Should(ContainSubstring(`desired="2"`))
		Expect(api.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		Expect(p.ResourceVersion).To(Equal(before), "envtest has no controller to update this Pod")
		p.Status.Phase = corev1.PodRunning
		Expect(api.Status().Update(ctx, p)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, ns) }).Should(And(
			ContainSubstring(`pod_phase="Running"`), ContainSubstring(`desired="2"`), ContainSubstring(`observation_valid="true"`)))
	})
	It("refreshes intermediate reparenting and handles same-name UID replacement without stale metadata", func(ctx SpecContext) {
		ns := namespace(ctx)
		first := deployment(ctx, ns, "first")
		second := deployment(ctx, ns, "second")
		rs := replicaSet(ctx, first, "middle")
		p := pod(ctx, rs, "moving")
		_, url, _ := start(defaults(ns))
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`workload_uid="` + string(first.UID) + `"`))
		rs.OwnerReferences[0].Name = second.Name
		rs.OwnerReferences[0].UID = second.UID
		Expect(api.Update(ctx, rs)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`workload_uid="` + string(second.UID) + `"`))
		Expect(api.Delete(ctx, rs, client.GracePeriodSeconds(0))).To(Succeed())
		Eventually(func() error { return api.Get(ctx, client.ObjectKeyFromObject(rs), &appsv1.ReplicaSet{}) }).ShouldNot(Succeed())
		replacement := replicaSet(ctx, first, "middle")
		Expect(api.Get(ctx, client.ObjectKeyFromObject(p), p)).To(Succeed())
		p.OwnerReferences[0].UID = replacement.UID
		Expect(api.Update(ctx, p)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`workload_uid="` + string(first.UID) + `"`))
		Expect(api.Delete(ctx, p, client.GracePeriodSeconds(0))).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(BeEmpty())
	})
	It("changes interpretation after Karta addition update and deletion without losing generic identity", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "root")
		p := pod(ctx, replicaSet(ctx, root, "middle"), "pod")
		cfg := defaults(ns)
		cfg.KartaDefinitionSource.Catalog.Enabled = false
		cfg.KartaDefinitionSource.Cluster.Enabled = true
		_, url, _ := start(cfg)
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`component="<unknown>"`))
		definition, err := catalog.Get(deployGVK)
		Expect(err).NotTo(HaveOccurred())
		definition.Name = "integration-" + ns
		Expect(api.Create(ctx, definition)).To(Succeed())
		DeferCleanup(func() { _ = api.Delete(context.Background(), definition) })
		Eventually(func() string { return scrape(url) }).Should(ContainSubstring(`karta="` + definition.Name + `"`))
		Expect(api.Get(ctx, types.NamespacedName{Name: definition.Name}, definition)).To(Succeed())
		definition.Spec.StructureDefinition.RootComponent.Name = "renamed"
		definition.Spec.Instructions.GangScheduling = nil
		definition.Spec.StructureDefinition.ChildComponents[0].OwnerRef = ptr.To("renamed")
		Expect(api.Update(ctx, definition)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`component="renamed"`))
		Expect(api.Delete(ctx, definition)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`component="<unknown>"`))
	})
	It("invalidates an interrupted intermediate watch and recovers after permission returns", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "root")
		rs := replicaSet(ctx, root, "middle")
		p := pod(ctx, rs, "pod")
		_, url, seen := start(defaults(ns))
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`observation_valid="true"`))
		seen.denyRequests("/replicasets")
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(And(ContainSubstring(`observation_valid="false"`), ContainSubstring(`owner_resolution="resolved"`)))
		Eventually(func() string { return scrape(url) }).Should(ContainSubstring(`reason="permission_denied"`))
		seen.denyRequests("")
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`observation_valid="true"`))
	})
	It("keeps unrelated roots available when a required API is absent", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "root")
		cfg := defaults(ns)
		cfg.RequiredKinds = []schema.GroupVersionKind{{Group: "absent.example.test", Version: "v1", Kind: "Missing"}}
		_, url, _ := start(cfg)
		Eventually(func() string { return scrape(url) }).Should(And(ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`resource_kind="Missing"`)))
		response, err := http.Get(url + "/readyz")
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusServiceUnavailable))
	})
	It("uses one cluster-wide Pod watch for empty namespace selectors", func(ctx SpecContext) {
		ns := namespace(ctx)
		deployment(ctx, ns, "root")
		cfg := defaults(ns)
		cfg.NamespaceSelector = &metav1.LabelSelector{}
		application, _, seen := start(cfg)
		Eventually(application.Runtime.Ready).Should(BeTrue())
		seen.mu.Lock()
		defer seen.mu.Unlock()
		Expect(seen.watches["/api/v1/pods"]).To(Equal(1))
		// Other fixtures can reference a Namespace as a generic workload root.
		// Traversal may legitimately watch it even without namespace selection.
	})
	It("keeps quiet paths valid without periodic metadata refresh", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "quiet")
		p := pod(ctx, replicaSet(ctx, root, "quiet-middle"), "quiet-pod")
		cfg := defaults(ns)
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
		_, url, _ := start(cfg)
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`observation_valid="true"`))
		Consistently(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }, 3*time.Second, 50*time.Millisecond).Should(ContainSubstring(`observation_valid="true"`))
	})
	It("releases namespaced watches and Pod rows when the namespace stops matching", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "selected")
		p := pod(ctx, replicaSet(ctx, root, "middle"), "selected-pod")
		cfg := defaults(ns)
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"exporter-test": "selected", "kubernetes.io/metadata.name": ns}}
		_, url, _ := start(cfg)
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).ShouldNot(BeEmpty())
		namespaceObject := &corev1.Namespace{}
		Expect(api.Get(ctx, types.NamespacedName{Name: ns}, namespaceObject)).To(Succeed())
		delete(namespaceObject.Labels, "exporter-test")
		Expect(api.Update(ctx, namespaceObject)).To(Succeed())
		Eventually(func() string { return scrape(url) }).ShouldNot(ContainSubstring(`workload_uid="` + string(root.UID) + `"`))
		namespaceObject.Labels["exporter-test"] = "selected"
		Expect(api.Update(ctx, namespaceObject)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(ContainSubstring(`observation_valid="true"`))
	})
	It("isolates an invalid Deployment definition from a valid Job", func(ctx SpecContext) {
		ns := namespace(ctx)
		deployment(ctx, ns, "invalid")
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: ns}, Spec: batchv1.JobSpec{Suspend: ptr.To(true), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "example.test/image:v1"}}}}}}
		Expect(api.Create(ctx, job)).To(Succeed())
		definition, err := catalog.Get(deployGVK)
		Expect(err).NotTo(HaveOccurred())
		definition.Name = "invalid-" + ns
		definition.Labels = map[string]string{"test": ns}
		definition.Spec.StructureDefinition.RootComponent.Name = ""
		Expect(api.Create(ctx, definition)).To(Succeed())
		DeferCleanup(func() { _ = api.Delete(context.Background(), definition) })
		cfg := defaults(ns)
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
		cfg.KartaDefinitionSource.Catalog.Include = []string{"batch-job-v1"}
		cfg.KartaDefinitionSource.Cluster.Enabled = true
		cfg.KartaDefinitionSource.Cluster.LabelSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"test": ns}}
		_, url, _ := start(cfg)
		Eventually(func() string { return scrape(url) }).Should(And(ContainSubstring(`workload_uid="`+string(job.UID)+`"`), ContainSubstring(`reason="invalid_definition"`)))
		Consistently(func() string {
			for _, line := range strings.Split(scrape(url), "\n") {
				if strings.HasPrefix(line, "karta_workload_status{") && strings.Contains(line, `workload_uid="`+string(job.UID)+`"`) {
					return line
				}
			}
			return ""
		}, time.Second).Should(ContainSubstring(`observation_valid="true"`))
	})

	It("reports a denied initial intermediate without inventing a root and recovers generic identity", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "unknown-root")
		p := pod(ctx, replicaSet(ctx, root, "denied-middle"), "initial-denied")
		cfg := defaults(ns)
		cfg.KartaDefinitionSource.Catalog.Enabled = false
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
		_, url, seen := start(cfg, "/replicasets")
		Eventually(func() string { return scrape(url) }).Should(ContainSubstring(`reason="permission_denied",resource_group="apps",resource_kind="ReplicaSet"`))
		Expect(infoFor(scrape(url), p.Name, p.Namespace)).To(BeEmpty())
		seen.denyRequests("")
		Eventually(func() string { return infoFor(scrape(url), p.Name, p.Namespace) }).Should(And(ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`component="<unknown>"`), ContainSubstring(`component_instance="<unknown>"`), ContainSubstring(`observation_valid="true"`)))
	})
	It("keeps verified creation while a selected definition source fails", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "retained")
		pod(ctx, replicaSet(ctx, root, "middle"), "retained-pod")
		definition, err := catalog.Get(deployGVK)
		Expect(err).NotTo(HaveOccurred())
		definition.Name = "retained-" + ns
		definition.Labels = map[string]string{"test": ns}
		Expect(api.Create(ctx, definition)).To(Succeed())
		DeferCleanup(func() { _ = api.Delete(context.Background(), definition) })
		cfg := defaults(ns)
		cfg.KartaDefinitionSource.Catalog.Enabled = false
		cfg.KartaDefinitionSource.Cluster.Enabled = true
		cfg.KartaDefinitionSource.Cluster.LabelSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"test": ns}}
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": ns}}
		_, url, seen := start(cfg)
		status := func() string {
			for _, line := range strings.Split(scrape(url), "\n") {
				if strings.HasPrefix(line, "karta_workload_status{") && strings.Contains(line, `workload_uid="`+string(root.UID)+`"`) {
					return line
				}
			}
			return ""
		}
		Eventually(status).Should(ContainSubstring(`observation_valid="true"`))
		seen.denyRequests("/kartas")
		Eventually(status).Should(And(ContainSubstring(`observation_valid="false"`), ContainSubstring(`owner_resolution="resolved"`)))
		Consistently(func() bool {
			for _, line := range strings.Split(scrape(url), "\n") {
				if strings.HasPrefix(line, "karta_workload_created_timestamp_seconds{") && strings.Contains(line, `workload_uid="`+string(root.UID)+`"`) {
					return true
				}
			}
			return false
		}, time.Second).Should(BeTrue())
		seen.denyRequests("")
		Eventually(status).Should(ContainSubstring(`observation_valid="true"`))
	})
	It("discovers a new root and its creation time while the initial Karta list is denied", func(ctx SpecContext) {
		ns := namespace(ctx)
		root := deployment(ctx, ns, "initial-karta-denial")
		p := pod(ctx, replicaSet(ctx, root, "initial-karta-middle"), "initial-karta-pod")
		definition, err := catalog.Get(deployGVK)
		Expect(err).NotTo(HaveOccurred())
		definition.Name = "initial-karta-" + ns
		definition.Labels = map[string]string{"test": ns}
		Expect(api.Create(ctx, definition)).To(Succeed())
		DeferCleanup(func() { _ = api.Delete(context.Background(), definition) })
		cfg := defaults(ns)
		cfg.KartaDefinitionSource.Catalog.Enabled = false
		cfg.KartaDefinitionSource.Cluster.Enabled = true
		cfg.KartaDefinitionSource.Cluster.LabelSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"test": ns}}
		_, url, seen := start(cfg, "/kartas")
		Eventually(func(g Gomega) {
			text := scrape(url)
			found := false
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "karta_workload_created_timestamp_seconds{") && strings.Contains(line, `workload_uid="`+string(root.UID)+`"`) {
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "ownership and creation do not require Karta status evaluation")
			g.Expect(text).To(ContainSubstring(`reason="permission_denied"`))
			g.Expect(infoFor(text, p.Name, ns)).NotTo(ContainSubstring(`observation_valid="true"`))
		}).Should(Succeed())
		seen.denyRequests("")
		Eventually(func(g Gomega) {
			text := scrape(url)
			g.Expect(infoFor(text, p.Name, ns)).To(And(ContainSubstring(`workload_uid="`+string(root.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
			g.Expect(text).To(ContainSubstring(`karta="` + definition.Name + `"`))
		}).Should(Succeed())
	})
	It("removes only exiting-namespace Pods below a shared cluster-scoped root", func(ctx SpecContext) {
		owner := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "cluster-root-"}}
		Expect(api.Create(ctx, owner)).To(Succeed())
		first, second := namespace(ctx), namespace(ctx)
		makePod := func(ns string) *corev1.Pod {
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "shared-name", Namespace: ns, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: owner.Name, UID: owner.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "example.test/image:v1"}}}}
			Expect(api.Create(ctx, p)).To(Succeed())
			return p
		}
		a, b := makePod(first), makePod(second)
		cfg := defaults(first)
		cfg.KartaDefinitionSource.Catalog.Enabled = false
		cfg.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"exporter-test": "selected"}, MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpIn, Values: []string{first, second}}}}
		_, url, _ := start(cfg)
		Eventually(func() string { return infoFor(scrape(url), a.Name, a.Namespace) }).Should(ContainSubstring(`workload_uid="` + string(owner.UID) + `"`))
		Eventually(func() string { return infoFor(scrape(url), b.Name, b.Namespace) }).Should(ContainSubstring(`workload_uid="` + string(owner.UID) + `"`))
		namespaceObject := &corev1.Namespace{}
		Expect(api.Get(ctx, types.NamespacedName{Name: first}, namespaceObject)).To(Succeed())
		delete(namespaceObject.Labels, "exporter-test")
		Expect(api.Update(ctx, namespaceObject)).To(Succeed())
		Eventually(func() string { return infoFor(scrape(url), a.Name, a.Namespace) }).Should(BeEmpty())
		Consistently(func() string { return infoFor(scrape(url), b.Name, b.Namespace) }, time.Second).Should(And(ContainSubstring(`workload_uid="`+string(owner.UID)+`"`), ContainSubstring(`observation_valid="true"`)))
	})

})
