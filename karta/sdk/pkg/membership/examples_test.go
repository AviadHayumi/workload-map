// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package membership

import (
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	api "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/references"
)

func example(t *testing.T, name string) (*api.Karta, *unstructured.Unstructured) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "examples", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	def := &api.Karta{}
	if err := yaml.UnmarshalStrict(data, def); err != nil {
		t.Fatal(err)
	}
	if err := api.NewKartaValidator(def).Validate(); err != nil {
		t.Fatal(err)
	}
	root := object(def.Spec.StructureDefinition.RootComponent.Kind.Kind, name)
	kind := def.Spec.StructureDefinition.RootComponent.Kind
	root.SetAPIVersion(kind.Group + "/" + kind.Version)
	return def, root
}
func TestMilvusDependencyKindsAndEligibility(t *testing.T) {
	for _, tc := range []struct{ role, apiVersion, kind string }{
		{"etcd", "apps/v1", "StatefulSet"}, {"etcd", "batch/v1", "CronJob"},
		{"minio", "apps/v1", "Deployment"}, {"minio", "apps/v1", "StatefulSet"},
		{"pulsar", "apps/v1", "StatefulSet"}, {"pulsar", "apps/v1", "Deployment"}, {"pulsar", "batch/v1", "Job"}, {"pulsar", "apps/v1", "DaemonSet"},
		{"kafka", "apps/v1", "StatefulSet"}, {"kafka", "apps/v1", "Deployment"}, {"tei", "apps/v1", "StatefulSet"},
	} {
		t.Run(tc.role+"-"+tc.kind, func(t *testing.T) {
			def, root := example(t, "milvus")
			root.Object["spec"] = map[string]any{"dependencies": map[string]any{"msgStreamType": tc.role, "tei": map[string]any{"enabled": true}}}
			target := object(tc.kind, "fullname-override")
			target.SetAPIVersion(tc.apiVersion)
			target.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
			target.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "milvus-" + tc.role, "meta.helm.sh/release-namespace": "test"})
			input := Input{Workloads: []WorkloadInput{{root, def}}, Pods: []corev1.Pod{pod("p", target)}, ScopeComplete: true}
			reader := &memoryReader{objects: []*unstructured.Unstructured{target}}
			result, err := Resolve(t.Context(), input, WithReader(reader))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Complete || len(result.Pods[ComponentKey{root.GetUID(), tc.role}]) != 1 || !result.Targets[target.GetUID()].Foldable {
				t.Fatalf("membership missing: complete=%v diagnostics=%v targets=%d", result.Complete, result.Diagnostics, len(result.Targets))
			}
			target.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "milvus-" + tc.role, "meta.helm.sh/release-namespace": "test", "helm.sh/hook": "post-install"})
			result, err = Resolve(t.Context(), input, WithReader(reader))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Targets) != 0 {
				t.Fatal("Helm hook was claimed")
			}
			// Switching the source off removes prior membership on the next observation.
			deps := root.Object["spec"].(map[string]any)["dependencies"].(map[string]any)
			switch tc.role {
			case "minio":
				deps["storage"] = map[string]any{"external": true}
			case "etcd":
				deps["etcd"] = map[string]any{"external": true}
			case "tei":
				deps["tei"] = map[string]any{"enabled": false}
			default:
				deps[tc.role] = map[string]any{"external": true}
			}
			target.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "milvus-" + tc.role, "meta.helm.sh/release-namespace": "test"})
			result, err = Resolve(t.Context(), input, WithReader(reader))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Targets) != 0 {
				t.Fatal("inactive dependency retained")
			}
		})
	}
}
func TestStrimziPoolOwnerEdgeAndLabelChange(t *testing.T) {
	def, root := example(t, "strimzi")
	pool := object("KafkaNodePool", "dual-role")
	pool.SetAPIVersion("kafka.strimzi.io/v1")
	pool.SetLabels(map[string]string{"strimzi.io/cluster": root.GetName()})
	podSet := object("StrimziPodSet", "pool-pods")
	podSet.SetAPIVersion("core.strimzi.io/v1beta2")
	podSet.SetOwnerReferences([]metav1.OwnerReference{owner(pool, false)})
	input := Input{Workloads: []WorkloadInput{{root, def}}, Pods: []corev1.Pod{pod("kafka-0", podSet)}, ScopeComplete: true}
	reader := &memoryReader{objects: []*unstructured.Unstructured{pool, podSet}}
	result, err := Resolve(t.Context(), input, WithReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pods[ComponentKey{root.GetUID(), "nodes"}]) != 1 {
		t.Fatal("pool Pod was not assigned exactly once")
	}
	pool.SetLabels(map[string]string{"strimzi.io/cluster": "other"})
	result, err = Resolve(t.Context(), input, WithReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 0 {
		t.Fatal("old pool label retained membership")
	}
}
func TestFlinkSharedClusterIsValueOnly(t *testing.T) {
	def, root := example(t, "flink")
	root.Object["spec"] = map[string]any{"deploymentName": "shared"}
	reader := &memoryReader{fail: true}
	result, err := Resolve(t.Context(), Input{Workloads: []WorkloadInput{{root, def}}, ScopeComplete: true}, WithReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || reader.calls != 0 || len(result.Targets) != 0 {
		t.Fatal("value-only cluster reference became membership")
	}
}
func TestTrainerRuntimeDoesNotMergeSeparateJobs(t *testing.T) {
	def, first := example(t, "trainer")
	first.SetUID("train-one")
	second := first.DeepCopy()
	second.SetName("two")
	second.SetUID("train-two")
	first.Object["spec"] = map[string]any{"runtimeRef": map[string]any{"name": "shared"}}
	second.Object["spec"] = first.Object["spec"]
	input := Input{Workloads: []WorkloadInput{{first, def}, {second, def}}, ScopeComplete: true}
	snapshot := Snapshot{Complete: true, References: map[types.UID]references.ResolvedReferences{}}
	for _, root := range []*unstructured.Unstructured{first, second} {
		js := object("JobSet", string(root.GetUID())+"-js")
		js.SetAPIVersion("jobset.x-k8s.io/v1alpha2")
		js.SetOwnerReferences([]metav1.OwnerReference{owner(root, true)})
		job := object("Job", string(root.GetUID())+"-job")
		job.SetAPIVersion("batch/v1")
		job.SetOwnerReferences([]metav1.OwnerReference{owner(js, true)})
		snapshot.Owners = append(snapshot.Owners, js, job)
		input.Pods = append(input.Pods, pod(string(root.GetUID())+"-pod", job))
	}
	result, err := Resolve(t.Context(), input, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 0 || len(result.Pods[ComponentKey{first.GetUID(), "trainer"}]) != 1 || len(result.Pods[ComponentKey{second.GetUID(), "trainer"}]) != 1 {
		t.Fatal("shared runtime merged independent TrainJobs")
	}
}
func TestKServeLabelsRequireOwnership(t *testing.T) {
	def, root := example(t, "kserve")
	other := object("InferenceService", "other")
	other.SetAPIVersion(root.GetAPIVersion())
	predictor := pod("predictor", root)
	predictor.Labels = map[string]string{"component": "predictor"}
	transformer := pod("transformer", root)
	transformer.Labels = map[string]string{"component": "transformer"}
	unrelated := pod("unrelated", other)
	unrelated.Labels = predictor.Labels
	result, err := Resolve(t.Context(), Input{Workloads: []WorkloadInput{{root, def}}, Pods: []corev1.Pod{predictor, transformer, unrelated}, ScopeComplete: true}, WithSnapshot(Snapshot{Owners: []*unstructured.Unstructured{other}, Complete: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pods[ComponentKey{root.GetUID(), "predictor"}]) != 1 || len(result.Pods[ComponentKey{root.GetUID(), "transformer"}]) != 1 {
		t.Fatal("label-only foreign Pod matched")
	}
}
func TestDeploymentHealthReferenceDoesNotBroadenMembership(t *testing.T) {
	def, root := example(t, "deployment")
	root.Object["spec"] = map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "shared"}}}
	own := pod("own", root)
	own.Labels = map[string]string{"app": "shared"}
	unowned := own.DeepCopy()
	unowned.Name = "unowned"
	unowned.UID = "unowned"
	unowned.OwnerReferences = nil
	reader := &memoryReader{fail: true}
	result, err := Resolve(t.Context(), Input{Workloads: []WorkloadInput{{root, def}}, Pods: []corev1.Pod{own, *unowned}, ScopeComplete: true}, WithReader(reader))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || reader.calls != 0 || len(result.Pods[ComponentKey{root.GetUID(), "replicas"}]) != 1 || len(result.Targets) != 0 {
		t.Fatal("health list broadened Pod membership")
	}
}
func TestMonitoringAddonSyntheticOwnerPaths(t *testing.T) {
	def, root := example(t, "milvus")
	raw, err := os.ReadFile("testdata/examples/milvus-monitoring.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var addon struct {
		References     []api.Reference    `json:"references"`
		FromReferences []api.PodReference `json:"fromReferences"`
	}
	if err := yaml.UnmarshalStrict(raw, &addon); err != nil {
		t.Fatal(err)
	}
	def.Spec.StructureDefinition.References = append(def.Spec.StructureDefinition.References, addon.References...)
	for i := range def.Spec.StructureDefinition.ChildComponents {
		c := &def.Spec.StructureDefinition.ChildComponents[i]
		if c.Name == "pulsar" {
			c.PodSelector.FromReferences = append(c.PodSelector.FromReferences, addon.FromReferences...)
		}
	}
	for _, tc := range []struct {
		kind, version string
		agent         bool
	}{{"Prometheus", "v1", false}, {"PrometheusAgent", "v1alpha1", true}, {"Alertmanager", "v1", false}, {"ThanosRuler", "v1", false}} {
		t.Run(tc.kind, func(t *testing.T) {
			root.Object["spec"] = map[string]any{"dependencies": map[string]any{"msgStreamType": "pulsar", "pulsar": map[string]any{"inCluster": map[string]any{"values": map[string]any{"kube-prometheus-stack": map[string]any{"enabled": true, "prometheus": map[string]any{"agentMode": tc.agent}, "thanosRuler": map[string]any{"enabled": true}}}}}}}
			target := object(tc.kind, "monitor")
			target.SetAPIVersion("monitoring.coreos.com/" + tc.version)
			target.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "Helm"})
			target.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "milvus-pulsar", "meta.helm.sh/release-namespace": "test"})
			sts := object("StatefulSet", "monitor-sts")
			sts.SetAPIVersion("apps/v1")
			sts.SetOwnerReferences([]metav1.OwnerReference{owner(target, true)})
			result, err := Resolve(t.Context(), Input{Workloads: []WorkloadInput{{root, def}}, Pods: []corev1.Pod{pod("monitor-pod", sts)}, ScopeComplete: true}, WithReader(&memoryReader{objects: []*unstructured.Unstructured{target, sts}}))
			if err != nil {
				t.Fatal(err)
			}
			if !result.Complete || len(result.Pods[ComponentKey{root.GetUID(), "pulsar"}]) != 1 {
				t.Fatalf("synthetic monitoring path failed: %v", result.Diagnostics)
			}
		})
	}
}
