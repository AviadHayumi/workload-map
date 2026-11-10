//go:build e2e

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type liveSuite struct {
	ctx                            context.Context
	c                              client.Client
	namespace, kubeconfig, cluster string
	ownedPVs                       []string
}

type liveWriteCounter struct {
	client.Client
	writes int
}

func (c *liveWriteCounter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.writes++
	return c.Client.Patch(ctx, obj, patch, opts...)
}
func (c *liveWriteCounter) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.writes++
	return c.Client.Create(ctx, obj, opts...)
}
func (c *liveWriteCounter) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.writes++
	return c.Client.Delete(ctx, obj, opts...)
}

func TestPluginsE2E(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	kubeconfig, cluster, namespace := os.Getenv("CEL_PLUGINS_KUBECONFIG"), os.Getenv("CEL_PLUGINS_CLUSTER"), os.Getenv("CEL_PLUGINS_E2E_NAMESPACE")
	if kubeconfig == "" {
		t.Skip("run scripts/e2e.sh with an explicit isolated kind kubeconfig")
	}
	if !strings.HasPrefix(cluster, "karta-cel-") || namespace == "" || len(validation.IsDNS1123Label(namespace)) != 0 {
		t.Fatal("a karta-cel-* cluster and a fresh valid namespace are required")
	}
	raw, err := clientcmd.LoadFromFile(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	if raw.CurrentContext != "kind-"+cluster {
		t.Fatalf("explicit kubeconfig context %q does not match owned cluster", raw.CurrentContext)
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, discoveryv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Minute)
	defer cancel()
	s := &liveSuite{ctx: ctx, c: c, namespace: namespace, kubeconfig: kubeconfig, cluster: cluster}
	ns := &corev1.Namespace{}
	if err := c.Get(ctx, types.NamespacedName{Name: namespace}, ns); !apierrors.IsNotFound(err) {
		t.Fatalf("namespace must not exist: %v", err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"plugins.karta.dev/e2e": "true"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retaining failed namespace %s for shell diagnostics", namespace)
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		if err := c.Delete(cleanupCtx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}); client.IgnoreNotFound(err) != nil {
			t.Error(err)
			return
		}
		if err := wait.PollUntilContextTimeout(cleanupCtx, time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
			err := c.Get(ctx, types.NamespacedName{Name: namespace}, &corev1.Namespace{})
			return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
		}); err != nil {
			t.Error("namespace cleanup:", err)
		}
		for _, name := range s.ownedPVs {
			if err := c.Delete(cleanupCtx, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name}}); client.IgnoreNotFound(err) != nil {
				t.Error(err)
			}
		}
	})
	s.install(t)
	s.clientPod(t)
	run := func(name string, check func(*testing.T)) {
		if !t.Run(name, check) {
			t.FailNow()
		}
	}
	const primary = "storage-web"
	marker := "original-" + namespace
	var claimUID, originalParentUID types.UID
	var claimName string
	run("01_native_deployment_storage_node_http", func(t *testing.T) {
		dep := liveDeployment(primary, map[string]string{annotationStorage: "1Gi", annotationPool: "blue", annotationService: "80"}, marker)
		s.create(t, dep)
		o := s.applied(t, primary, "Deployment")
		originalParentUID = o.GetUID()
		saved, err := readState(o)
		if err != nil || saved.Storage == nil || saved.Storage.UID == "" {
			t.Fatalf("uncommitted storage: %+v %v", saved, err)
		}
		claimUID = saved.Storage.UID
		claimName, _, err = childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.staticPV(t, claimName, "primary")
		s.editDeployment(t, primary, func(d *appsv1.Deployment) { liveMount(d, claimName); *d.Spec.Replicas = 1 })
		pod := s.deploymentReady(t, primary, "blue", "")
		s.checkClaim(t, claimName, claimUID, pod)
		_, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.http(t, svcName, 80, marker)
		s.endpoint(t, svcName, pod.UID)
	})
	run("02_service_update_drift_recreation_defaults", func(t *testing.T) {
		o := s.parent(t, primary, "Deployment")
		_, name, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.editService(t, name, func(svc *corev1.Service) {
			if svc.Annotations == nil {
				svc.Annotations = map[string]string{}
			}
			svc.Annotations["example.test/user"] = "keep"
			svc.Spec.Ports[0].AppProtocol = livePtr("http")
			svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "metrics", Port: 9090, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(8080)})
		})
		before := s.service(t, name)
		s.setAnnotations(t, primary, "Deployment", map[string]string{annotationService: "8081"})
		s.applied(t, primary, "Deployment")
		s.servicePort(t, name, 8081)
		after := s.service(t, name)
		if before.Spec.ClusterIP != after.Spec.ClusterIP || !reflect.DeepEqual(before.Spec.ClusterIPs, after.Spec.ClusterIPs) || !reflect.DeepEqual(before.Spec.IPFamilies, after.Spec.IPFamilies) || !reflect.DeepEqual(before.Spec.IPFamilyPolicy, after.Spec.IPFamilyPolicy) || !reflect.DeepEqual(before.Spec.Ports[1], after.Spec.Ports[1]) || after.Annotations["example.test/user"] != "keep" || after.Spec.Ports[0].AppProtocol == nil || *after.Spec.Ports[0].AppProtocol != "http" {
			t.Fatal("Service update lost allocations, defaults or unrelated fields")
		}
		s.http(t, name, 8081, marker)
		s.editService(t, name, func(svc *corev1.Service) {
			svc.Spec.Selector = map[string]string{"foreign": "drift"}
			svc.Spec.Ports[0].Port = 9000
			svc.Spec.Ports[0].TargetPort = intstr.FromInt32(9001)
			svc.Spec.Ports[0].Protocol = corev1.ProtocolUDP
		})
		s.servicePort(t, name, 8081)
		s.http(t, name, 8081, marker)
		oldUID := s.service(t, name).UID
		s.delete(t, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: oldUID}}, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &oldUID}})
		s.until(t, "Service recreated with a new UID", func() error {
			svc := &corev1.Service{}
			if err := s.c.Get(s.ctx, s.key(name), svc); err != nil {
				return err
			}
			if svc.UID == oldUID {
				return errors.New("old Service UID remains")
			}
			return nil
		})
		s.servicePort(t, name, 8081)
		s.http(t, name, 8081, marker)
	})
	run("03_storage_free_cross_node_placement", func(t *testing.T) {
		dep := liveDeployment("mobile", map[string]string{annotationPool: "blue"}, "mobile")
		s.create(t, dep)
		s.applied(t, "mobile", "Deployment")
		s.editDeployment(t, "mobile", func(d *appsv1.Deployment) { *d.Spec.Replicas = 1 })
		old := s.deploymentReady(t, "mobile", "blue", "")
		s.setAnnotations(t, "mobile", "Deployment", map[string]string{annotationPool: "green"})
		s.applied(t, "mobile", "Deployment")
		next := s.deploymentReady(t, "mobile", "green", old.UID)
		if old.Spec.NodeName == next.Spec.NodeName {
			t.Fatal("placement update did not move the Pod across workers")
		}
	})
	run("04_cronjob_future_job_inheritance", func(t *testing.T) {
		dep := liveDeployment("cron-web", nil, "cron-template")
		cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: primary, Namespace: namespace, Annotations: map[string]string{annotationPool: "blue", annotationService: "80"}}, Spec: batchv1.CronJobSpec{Schedule: "0 0 * * *", Suspend: livePtr(true), ConcurrencyPolicy: batchv1.ForbidConcurrent, JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Parallelism: livePtr(int32(1)), BackoffLimit: livePtr(int32(0)), Template: dep.Spec.Template}}}}
		cj.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
		s.create(t, cj)
		o := s.applied(t, primary, "CronJob")
		_, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.kubectl(t, "create", "job", "--from=cronjob/"+primary, "cron-first")
		firstPod := s.jobReady(t, "cron-first", "blue")
		s.http(t, svcName, 80, "cron-template")
		s.endpoint(t, svcName, firstPod.UID)
		first := &batchv1.Job{}
		s.get(t, "cron-first", first)
		firstSpec := first.Spec.Template.DeepCopy()
		s.setAnnotations(t, primary, "CronJob", map[string]string{annotationPool: "green", annotationService: "8082"})
		s.applied(t, primary, "CronJob")
		s.kubectl(t, "create", "job", "--from=cronjob/"+primary, "cron-second")
		s.jobReady(t, "cron-second", "green")
		s.get(t, "cron-first", first)
		if !reflect.DeepEqual(firstSpec, &first.Spec.Template) {
			t.Fatal("CronJob update changed an earlier Job")
		}
		s.http(t, svcName, 8082, "cron-template")
		s.delete(t, first)
		second := &batchv1.Job{}
		s.get(t, "cron-second", second)
		s.delete(t, second)
	})
	run("05_opt_out_reenable_retains_data_and_native_mounts", func(t *testing.T) {
		before := s.deploymentReady(t, primary, "blue", "")
		s.editDeployment(t, primary, func(d *appsv1.Deployment) {
			d.Spec.Template.Spec.Containers[0].Env[0].Value = "replacement-must-not-overwrite"
		})
		for _, annotation := range []string{annotationPool, annotationService, annotationStorage} {
			s.removeAnnotations(t, primary, "Deployment", annotation)
			s.applied(t, primary, "Deployment")
		}
		o := s.parent(t, primary, "Deployment")
		saved, err := readState(o)
		if err != nil || saved.Storage == nil || saved.Storage.UID != claimUID || saved.Pool != "" || saved.Service {
			t.Fatalf("opt-out state: %+v %v", saved, err)
		}
		_, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.absent(t, svcName, &corev1.Service{})
		s.setAnnotations(t, primary, "Deployment", map[string]string{annotationStorage: "1Gi", annotationPool: "blue", annotationService: "80"})
		s.applied(t, primary, "Deployment")
		pod := s.deploymentReady(t, primary, "blue", before.UID)
		s.checkClaim(t, claimName, claimUID, pod)
		s.http(t, svcName, 80, marker)
		dep := &appsv1.Deployment{}
		s.get(t, primary, dep)
		if dep.Spec.Template.Spec.NodeSelector["kubernetes.io/os"] != "linux" || len(dep.Spec.Template.Spec.Containers[0].VolumeMounts) != 2 || len(dep.Spec.Template.Spec.Volumes) != 2 {
			t.Fatal("plugin removal changed native mounts or unrelated placement")
		}
	})
	run("06_normal_and_seeded_state_restart", func(t *testing.T) {
		s.controller(t, 0)
		s.setAnnotations(t, primary, "Deployment", map[string]string{annotationService: "8083"})
		o := s.parent(t, primary, "Deployment")
		_, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.delete(t, s.service(t, svcName))
		s.controller(t, 1)
		s.applied(t, primary, "Deployment")
		s.servicePort(t, svcName, 8083)
		s.http(t, svcName, 8083, marker)
		savedBeforeRestart, err := readState(s.parent(t, primary, "Deployment"))
		if err != nil || savedBeforeRestart.Storage == nil || savedBeforeRestart.Storage.UID != claimUID {
			t.Fatalf("normal restart changed claim commitment: %+v %v", savedBeforeRestart, err)
		}
		s.checkClaim(t, claimName, claimUID, s.deploymentReady(t, primary, "blue", ""))
		s.controller(t, 0)
		for _, present := range []bool{true, false} {
			name := "pending-absent"
			if present {
				name = "pending-present"
			}
			s.create(t, liveDeployment(name, map[string]string{annotationStorage: "1Gi"}, name))
			parent := s.parent(t, name, "Deployment")
			saved := state{Version: 1, WorkloadUID: parent.GetUID(), Storage: &storageState{Size: "1Gi", StorageClass: ""}}
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := s.c.Get(s.ctx, s.key(name), parent); err != nil {
					return err
				}
				if err := saveState(parent, saved); err != nil {
					return err
				}
				return s.c.Update(s.ctx, parent)
			}); err != nil {
				t.Fatal(err)
			}
			if present {
				pvcName, _, err := childNames(parent.GetUID())
				if err != nil {
					t.Fatal(err)
				}
				s.create(t, desiredPVC(parent, pvcName, saved.Storage))
			}
		}
		s.controller(t, 1)
		present := s.applied(t, "pending-present", "Deployment")
		saved, err := readState(present)
		if err != nil || saved.Storage == nil || saved.Storage.UID == "" {
			t.Fatalf("restart did not recover observed claim: %+v %v", saved, err)
		}
		absent := s.diagnostic(t, "pending-absent", "Deployment", "Error", "StorageCreationUncertain")
		pvcName, _, err := childNames(absent.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.absent(t, pvcName, &corev1.PersistentVolumeClaim{})
	})
	run("07_ownership_storage_and_input_failures", func(t *testing.T) { s.negativeCases(t, primary, claimName, claimUID) })
	run("08_parent_gc_retention_recreation_and_unwatched_job", func(t *testing.T) {
		s.editDeployment(t, primary, func(d *appsv1.Deployment) { *d.Spec.Replicas = 0 })
		s.noPods(t, map[string]string{"app": primary})
		o := s.parent(t, primary, "Deployment")
		_, oldSvcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.delete(t, o)
		s.absent(t, primary, &appsv1.Deployment{})
		s.absent(t, oldSvcName, &corev1.Service{})
		claim := &corev1.PersistentVolumeClaim{}
		s.get(t, claimName, claim)
		if claim.UID != claimUID {
			t.Fatal("parent deletion lost retained claim")
		}
		s.create(t, liveDeployment(primary, map[string]string{annotationStorage: "1Gi", annotationPool: "blue", annotationService: "80"}, "new-parent"))
		replacement := s.applied(t, primary, "Deployment")
		if replacement.GetUID() == originalParentUID {
			t.Fatal("recreated parent retained old UID")
		}
		newClaim, _, err := childNames(replacement.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		if newClaim == claimName {
			t.Fatal("new workload reused retained child names")
		}
		s.get(t, claimName, claim)
		if claim.UID != claimUID {
			t.Fatal("new parent changed retained claim")
		}
		probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "retained-reader", Namespace: namespace}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: livePtr(int64(1)), NodeSelector: map[string]string{poolKey: "blue"}, Containers: []corev1.Container{{Name: "reader", Image: "busybox:1.37.0", ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sh", "-ec", "sleep 3600"}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName, ReadOnly: true}}}}}}
		s.create(t, probe)
		s.podReady(t, "retained-reader", "blue")
		if got := strings.TrimSpace(s.kubectl(t, "exec", "retained-reader", "--", "cat", "/data/marker")); got != marker {
			t.Fatalf("retained data = %q", got)
		}
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "unwatched", Namespace: namespace, Annotations: map[string]string{annotationStorage: "1Gi", annotationPool: "blue", annotationService: "80"}}, Spec: batchv1.JobSpec{BackoffLimit: livePtr(int32(0)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "work", Image: "busybox:1.37.0", ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sh", "-ec", "echo standalone-job"}}}}}}}
		s.create(t, job)
		s.until(t, "unwatched Job completed", func() error {
			if err := s.c.Get(s.ctx, s.key("unwatched"), job); err != nil {
				return err
			}
			if job.Status.Succeeded != 1 {
				return errors.New("Job not complete")
			}
			return nil
		})
		if _, ok := job.Annotations[annotationResult]; ok {
			t.Fatal("unsupported Job received controller diagnostic")
		}
		if _, ok := job.Annotations[annotationState]; ok {
			t.Fatal("unsupported Job received state")
		}
		pvcName, svcName, err := childNames(job.UID)
		if err != nil {
			t.Fatal(err)
		}
		s.absent(t, pvcName, &corev1.PersistentVolumeClaim{})
		s.absent(t, svcName, &corev1.Service{})
		cron := s.parent(t, primary, "CronJob")
		_, cronService, err := childNames(cron.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.service(t, cronService)
	})
	run("09_real_api_conflict_and_noop_reconciliation", func(t *testing.T) {
		s.create(t, liveDeployment("optimistic", nil, "optimistic"))
		var base *unstructured.Unstructured
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			base = s.parent(t, "optimistic", "Deployment")
			fresh := base.DeepCopy()
			fresh.SetAnnotations(map[string]string{"example.test/user": "concurrent"})
			return s.c.Update(s.ctx, fresh)
		}); err != nil {
			t.Fatal(err)
		}
		stale := base.DeepCopy()
		stale.SetLabels(map[string]string{"example.test/stale": "rejected"})
		err := s.c.Patch(s.ctx, stale, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if !apierrors.IsConflict(err) {
			t.Fatalf("real stale resourceVersion patch error = %v", err)
		}
		o := s.applied(t, primary, "Deployment")
		_, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.servicePort(t, svcName, 80)
		before := s.service(t, svcName)
		programs, err := newPrograms()
		if err != nil {
			t.Fatal(err)
		}
		counter := &liveWriteCounter{Client: s.c}
		r := &reconciler{client: counter, reader: s.c, namespace: namespace, gvk: appsv1.SchemeGroupVersion.WithKind("Deployment"), programs: programs}
		for range 5 {
			if _, err := r.Reconcile(s.ctx, ctrl.Request{NamespacedName: s.key(primary)}); err != nil {
				t.Fatal(err)
			}
		}
		if counter.writes != 0 {
			t.Fatalf("converged real-API reconciliation issued %d writes", counter.writes)
		}
		after := s.service(t, svcName)
		if before.ResourceVersion != after.ResourceVersion || !reflect.DeepEqual(before.Spec, after.Spec) {
			t.Fatal("no-op changed Service resourceVersion or spec")
		}
		afterParent := s.parent(t, primary, "Deployment")
		if !reflect.DeepEqual(o.Object["spec"], afterParent.Object["spec"]) || o.GetAnnotations()[annotationState] != afterParent.GetAnnotations()[annotationState] || o.GetAnnotations()[annotationResult] != afterParent.GetAnnotations()[annotationResult] {
			t.Fatal("no-op changed parent configuration")
		}
	})
}

func livePtr[T any](value T) *T { return &value }

func liveDeployment(name string, annotations map[string]string, marker string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
		Spec: appsv1.DeploymentSpec{
			Replicas: livePtr(int32(0)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name, "example.test/user": "keep"}},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: livePtr(int64(1)),
					NodeSelector:                  map[string]string{"kubernetes.io/os": "linux"},
					Containers: []corev1.Container{
						{
							Name: "web", Image: "busybox:1.37.0", ImagePullPolicy: corev1.PullIfNotPresent,
							Command: []string{"sh", "-ec"},
							Args: []string{`root=/tmp/www
test ! -d /data || root=/data
mkdir -p "$root"
test -f "$root/marker" || printf '%s\n' "$INITIAL_MARKER" > "$root/marker"
exec httpd -f -p 8080 -h "$root"`},
							Env:          []corev1.EnvVar{{Name: "INITIAL_MARKER", Value: marker}},
							Ports:        []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
							VolumeMounts: []corev1.VolumeMount{{Name: "extra", MountPath: "/extra"}},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/marker", Port: intstr.FromString("http")}},
								PeriodSeconds: 1,
							},
						},
						{Name: "unrelated", Image: "busybox:1.37.0", ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sh", "-ec", "sleep 3600"}},
					},
					Volumes: []corev1.Volume{{Name: "extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				},
			},
		},
	}
}

func liveMount(dep *appsv1.Deployment, claim string) {
	dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}})
	dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: "/data"})
}

func (s *liveSuite) key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: s.namespace, Name: name}
}
func (s *liveSuite) create(t *testing.T, obj client.Object) {
	t.Helper()
	if obj.GetNamespace() == "" {
		obj.SetNamespace(s.namespace)
	}
	if err := s.c.Create(s.ctx, obj); err != nil {
		t.Fatal(err)
	}
}
func (s *liveSuite) get(t *testing.T, name string, obj client.Object) {
	t.Helper()
	if err := s.c.Get(s.ctx, s.key(name), obj); err != nil {
		t.Fatal(err)
	}
}
func (s *liveSuite) delete(t *testing.T, obj client.Object, opts ...client.DeleteOption) {
	t.Helper()
	if err := s.c.Delete(s.ctx, obj, opts...); client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
}

func (s *liveSuite) until(t *testing.T, description string, observe func() error) {
	t.Helper()
	var last error
	err := wait.PollUntilContextTimeout(s.ctx, 300*time.Millisecond, 2*time.Minute, true, func(context.Context) (bool, error) { last = observe(); return last == nil, nil })
	if err != nil {
		t.Fatalf("%s: %v; last observation: %v", description, err, last)
	}
}

func (s *liveSuite) absent(t *testing.T, name string, obj client.Object) {
	t.Helper()
	s.until(t, name+" absent", func() error {
		err := s.c.Get(s.ctx, s.key(name), obj)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("object still exists")
	})
}

func (s *liveSuite) kubectl(t *testing.T, args ...string) string {
	t.Helper()
	output, err := s.command(args...)
	if err != nil {
		t.Fatalf("kubectl %v: %v: %s", args, err, output)
	}
	return output
}
func (s *liveSuite) command(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", s.kubeconfig, "--namespace", s.namespace}, args...)...).CombinedOutput()
	return string(output), err
}

func (s *liveSuite) install(t *testing.T) {
	t.Helper()
	for _, name := range []string{"01-rbac.yaml", "02-controller.yaml"} {
		data, err := os.ReadFile(filepath.Join("manifests", name))
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			var obj unstructured.Unstructured
			if err := decoder.Decode(&obj); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if obj.Object == nil {
				continue
			}
			obj.SetNamespace(s.namespace)
			if obj.GetKind() == "RoleBinding" {
				subjects, _, err := unstructured.NestedSlice(obj.Object, "subjects")
				if err != nil {
					t.Fatal(err)
				}
				for _, subject := range subjects {
					subject.(map[string]any)["namespace"] = s.namespace
				}
				if err := unstructured.SetNestedSlice(obj.Object, subjects, "subjects"); err != nil {
					t.Fatal(err)
				}
			}
			s.create(t, &obj)
		}
	}
	s.controllerReady(t)
}

func (s *liveSuite) controllerReady(t *testing.T) {
	t.Helper()
	s.until(t, "controller Deployment available", func() error {
		dep := &appsv1.Deployment{}
		if err := s.c.Get(s.ctx, s.key("cel-plugins"), dep); err != nil {
			return err
		}
		if dep.Status.ObservedGeneration < dep.Generation || dep.Status.AvailableReplicas != 1 || dep.Status.UpdatedReplicas != 1 {
			return errors.New("controller not ready")
		}
		return nil
	})
}

func (s *liveSuite) controller(t *testing.T, replicas int32) {
	t.Helper()
	s.editDeployment(t, "cel-plugins", func(d *appsv1.Deployment) { *d.Spec.Replicas = replicas })
	if replicas == 0 {
		s.noPods(t, map[string]string{"app": "cel-plugins"})
	} else {
		s.controllerReady(t)
	}
}

func (s *liveSuite) editDeployment(t *testing.T, name string, edit func(*appsv1.Deployment)) {
	t.Helper()
	s.until(t, "update Deployment "+name, func() error {
		d := &appsv1.Deployment{}
		if err := s.c.Get(s.ctx, s.key(name), d); err != nil {
			return err
		}
		edit(d)
		return s.c.Update(s.ctx, d)
	})
}
func (s *liveSuite) editService(t *testing.T, name string, edit func(*corev1.Service)) {
	t.Helper()
	s.until(t, "update Service "+name, func() error {
		svc := &corev1.Service{}
		if err := s.c.Get(s.ctx, s.key(name), svc); err != nil {
			return err
		}
		edit(svc)
		return s.c.Update(s.ctx, svc)
	})
}

func (s *liveSuite) parent(t *testing.T, name, kind string) *unstructured.Unstructured {
	t.Helper()
	o := &unstructured.Unstructured{}
	apiVersion := "apps/v1"
	if kind == "CronJob" {
		apiVersion = "batch/v1"
	}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	s.get(t, name, o)
	return o
}
func (s *liveSuite) setAnnotations(t *testing.T, name, kind string, values map[string]string) {
	t.Helper()
	s.until(t, "update input annotations", func() error {
		o := s.parent(t, name, kind)
		a := o.GetAnnotations()
		if a == nil {
			a = map[string]string{}
		}
		for k, v := range values {
			a[k] = v
		}
		o.SetAnnotations(a)
		return s.c.Update(s.ctx, o)
	})
}
func (s *liveSuite) removeAnnotations(t *testing.T, name, kind string, keys ...string) {
	t.Helper()
	s.until(t, "remove input annotations", func() error {
		o := s.parent(t, name, kind)
		a := o.GetAnnotations()
		for _, k := range keys {
			delete(a, k)
		}
		o.SetAnnotations(a)
		return s.c.Update(s.ctx, o)
	})
}

func (s *liveSuite) applied(t *testing.T, name, kind string) *unstructured.Unstructured {
	t.Helper()
	return s.diagnostic(t, name, kind, "Applied", "")
}
func (s *liveSuite) diagnostic(t *testing.T, name, kind, status, reason string) *unstructured.Unstructured {
	t.Helper()
	var o *unstructured.Unstructured
	s.until(t, name+" diagnostic "+status+"/"+reason, func() error {
		o = s.parent(t, name, kind)
		var result resultAnnotation
		if err := json.Unmarshal([]byte(o.GetAnnotations()[annotationResult]), &result); err != nil {
			return err
		}
		if result.State != status || result.Reason != reason || result.InputHash != inputHash(o, "") {
			return fmt.Errorf("got result %+v", result)
		}
		return nil
	})
	return o
}

func (s *liveSuite) service(t *testing.T, name string) *corev1.Service {
	t.Helper()
	svc := &corev1.Service{}
	s.get(t, name, svc)
	return svc
}
func (s *liveSuite) servicePort(t *testing.T, name string, port int32) {
	t.Helper()
	s.until(t, "Service port and selector converge", func() error {
		svc := &corev1.Service{}
		if err := s.c.Get(s.ctx, s.key(name), svc); err != nil {
			return err
		}
		for _, p := range svc.Spec.Ports {
			if p.Name == "http" && p.Port == port && p.TargetPort == intstr.FromInt32(8080) && p.Protocol == corev1.ProtocolTCP && len(svc.Spec.Selector) == 1 && svc.Spec.Selector[workloadUIDKey] == svc.Labels[workloadUIDKey] {
				return nil
			}
		}
		return errors.New("owned Service fields have not converged")
	})
}

func (s *liveSuite) clientPod(t *testing.T) {
	t.Helper()
	s.create(t, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "http-client"}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: livePtr(int64(1)), Containers: []corev1.Container{{Name: "client", Image: "busybox:1.37.0", ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sh", "-ec", "sleep 3600"}}}}})
	s.podReady(t, "http-client", "")
}
func (s *liveSuite) http(t *testing.T, name string, port int32, marker string) {
	t.Helper()
	s.until(t, "HTTP marker through cluster DNS and Service", func() error {
		output, err := s.command("exec", "http-client", "--", "wget", "-q", "-T", "2", "-O", "-", fmt.Sprintf("http://%s.%s.svc:%d/marker", name, s.namespace, port))
		if err != nil {
			return fmt.Errorf("%w: %s", err, output)
		}
		if strings.TrimSpace(output) != marker {
			return fmt.Errorf("marker = %q", output)
		}
		return nil
	})
}

func (s *liveSuite) podReady(t *testing.T, name, pool string) *corev1.Pod {
	t.Helper()
	var pod corev1.Pod
	s.until(t, "Pod "+name+" Ready on requested node", func() error {
		if err := s.c.Get(s.ctx, s.key(name), &pod); err != nil {
			return err
		}
		return s.readyOnPool(&pod, pool)
	})
	return &pod
}
func (s *liveSuite) readyOnPool(pod *corev1.Pod, pool string) error {
	if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
		return errors.New("Pod is terminating or unscheduled")
	}
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return errors.New("Pod is not Ready")
	}
	if pool != "" {
		node := &corev1.Node{}
		if err := s.c.Get(s.ctx, types.NamespacedName{Name: pod.Spec.NodeName}, node); err != nil {
			return err
		}
		if node.Labels[poolKey] != pool {
			return fmt.Errorf("node pool = %q", node.Labels[poolKey])
		}
	}
	return nil
}

func (s *liveSuite) deploymentReady(t *testing.T, name, pool string, oldUID types.UID) *corev1.Pod {
	t.Helper()
	var result *corev1.Pod
	s.until(t, "Deployment "+name+" rollout to "+pool, func() error {
		dep := &appsv1.Deployment{}
		if err := s.c.Get(s.ctx, s.key(name), dep); err != nil {
			return err
		}
		if dep.Status.ObservedGeneration < dep.Generation || dep.Status.UpdatedReplicas != 1 || dep.Status.AvailableReplicas != 1 {
			return errors.New("Deployment rollout incomplete")
		}
		pods := &corev1.PodList{}
		if err := s.c.List(s.ctx, pods, client.InNamespace(s.namespace), client.MatchingLabels{"app": name}); err != nil {
			return err
		}
		if len(pods.Items) != 1 {
			return fmt.Errorf("expected one active Pod, got %d", len(pods.Items))
		}
		pod := pods.Items[0]
		if pod.UID == oldUID {
			return errors.New("replacement Pod UID not observed")
		}
		if err := s.readyOnPool(&pod, pool); err != nil {
			return err
		}
		result = &pod
		return nil
	})
	return result
}
func (s *liveSuite) jobReady(t *testing.T, name, pool string) *corev1.Pod {
	t.Helper()
	var result *corev1.Pod
	s.until(t, "Job "+name+" Pod Ready", func() error {
		pods := &corev1.PodList{}
		if err := s.c.List(s.ctx, pods, client.InNamespace(s.namespace), client.MatchingLabels{"job-name": name}); err != nil {
			return err
		}
		if len(pods.Items) != 1 {
			return fmt.Errorf("Job Pods = %d", len(pods.Items))
		}
		pod := pods.Items[0]
		if err := s.readyOnPool(&pod, pool); err != nil {
			return err
		}
		result = &pod
		return nil
	})
	return result
}
func (s *liveSuite) noPods(t *testing.T, labels map[string]string) {
	t.Helper()
	s.until(t, "Pods terminated", func() error {
		pods := &corev1.PodList{}
		if err := s.c.List(s.ctx, pods, client.InNamespace(s.namespace), client.MatchingLabels(labels)); err != nil {
			return err
		}
		if len(pods.Items) != 0 {
			return fmt.Errorf("%d Pods remain", len(pods.Items))
		}
		return nil
	})
}

func (s *liveSuite) staticPV(t *testing.T, claim, name string) {
	t.Helper()
	pvName := s.namespace + "-" + name
	s.ownedPVs = append(s.ownedPVs, pvName)
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: pvName, Labels: map[string]string{"plugins.karta.dev/e2e": s.namespace}},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              "", VolumeMode: livePtr(corev1.PersistentVolumeFilesystem),
			ClaimRef:               &corev1.ObjectReference{Namespace: s.namespace, Name: claim},
			PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/local/" + pvName, Type: livePtr(corev1.HostPathDirectoryOrCreate)}},
			NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: poolKey, Operator: corev1.NodeSelectorOpIn, Values: []string{"blue"}}}},
			}}},
		},
	}
	if err := s.c.Create(s.ctx, pv); err != nil {
		t.Fatal(err)
	}
}
func (s *liveSuite) checkClaim(t *testing.T, name string, uid types.UID, pod *corev1.Pod) {
	t.Helper()
	claim := &corev1.PersistentVolumeClaim{}
	s.get(t, name, claim)
	if claim.UID != uid || claim.Status.Phase != corev1.ClaimBound || claim.Spec.VolumeName == "" {
		t.Fatalf("claim identity/binding not preserved: uid=%s phase=%s", claim.UID, claim.Status.Phase)
	}
	found := false
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == name {
			found = true
		}
	}
	if !found {
		t.Fatal("live Pod does not reference recorded claim")
	}
}
func (s *liveSuite) endpoint(t *testing.T, name string, uid types.UID) {
	t.Helper()
	s.until(t, "ready EndpointSlice targets observed Pod UID", func() error {
		slices := &discoveryv1.EndpointSliceList{}
		if err := s.c.List(s.ctx, slices, client.InNamespace(s.namespace), client.MatchingLabels{discoveryv1.LabelServiceName: name}); err != nil {
			return err
		}
		for _, slice := range slices.Items {
			for _, endpoint := range slice.Endpoints {
				if endpoint.TargetRef != nil && endpoint.TargetRef.UID == uid && endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
					return nil
				}
			}
		}
		return errors.New("expected ready Pod endpoint not found")
	})
}

func (s *liveSuite) negativeCases(t *testing.T, primary, claimName string, claimUID types.UID) {
	t.Helper()
	for _, tc := range []struct{ name, key, value string }{{"invalid-quantity", annotationStorage, "11Gi"}, {"invalid-port", annotationService, "bad"}, {"empty-pool", annotationPool, ""}} {
		s.create(t, liveDeployment(tc.name, map[string]string{tc.key: tc.value}, tc.name))
		o := s.diagnostic(t, tc.name, "Deployment", "Error", "InvalidInput")
		pvcName, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		s.absent(t, pvcName, &corev1.PersistentVolumeClaim{})
		s.absent(t, svcName, &corev1.Service{})
	}
	for _, kind := range []string{"pvc", "service"} {
		name := "foreign-" + kind
		s.create(t, liveDeployment(name, nil, name))
		o := s.parent(t, name, "Deployment")
		pvcName, svcName, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		var foreign client.Object
		key := annotationService
		if kind == "pvc" {
			key = annotationStorage
			foreign = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: s.namespace, Labels: map[string]string{"foreign": "keep"}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: livePtr(""), Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
		} else {
			foreign = &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: s.namespace, Labels: map[string]string{"foreign": "keep"}}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}}}
		}
		s.create(t, foreign)
		uid := foreign.GetUID()
		value := "80"
		if kind == "pvc" {
			value = "1Gi"
		}
		s.setAnnotations(t, name, "Deployment", map[string]string{key: value})
		s.diagnostic(t, name, "Deployment", "Error", "OwnershipConflict")
		s.get(t, foreign.GetName(), foreign)
		if foreign.GetUID() != uid || foreign.GetLabels()["foreign"] != "keep" {
			t.Fatal("foreign child changed")
		}
	}
	collision := liveDeployment("reserved-field", nil, "collision")
	collision.Spec.Template.Spec.NodeSelector[poolKey] = "blue"
	s.create(t, collision)
	s.setAnnotations(t, "reserved-field", "Deployment", map[string]string{annotationPool: "blue"})
	s.diagnostic(t, "reserved-field", "Deployment", "Error", "FieldConflict")
	s.setAnnotations(t, primary, "Deployment", map[string]string{annotationStorage: "2Gi"})
	s.diagnostic(t, primary, "Deployment", "Error", "StorageChangeUnsupported")
	claim := &corev1.PersistentVolumeClaim{}
	s.get(t, claimName, claim)
	size := claim.Spec.Resources.Requests[corev1.ResourceStorage]
	if claim.UID != claimUID || size.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Fatal("forbidden capacity change mutated claim")
	}
	s.setAnnotations(t, primary, "Deployment", map[string]string{annotationStorage: "1Gi"})
	s.applied(t, primary, "Deployment")
	for _, replace := range []bool{false, true} {
		name := "missing-claim"
		if replace {
			name = "replaced-claim"
		}
		s.create(t, liveDeployment(name, map[string]string{annotationStorage: "1Gi"}, name))
		o := s.applied(t, name, "Deployment")
		pvcName, _, err := childNames(o.GetUID())
		if err != nil {
			t.Fatal(err)
		}
		pvc := &corev1.PersistentVolumeClaim{}
		s.get(t, pvcName, pvc)
		oldUID := pvc.UID
		s.delete(t, pvc)
		s.absent(t, pvcName, &corev1.PersistentVolumeClaim{})
		if replace {
			pvc.ResourceVersion = ""
			pvc.UID = ""
			pvc.CreationTimestamp = metav1.Time{}
			pvc.ManagedFields = nil
			pvc.Status = corev1.PersistentVolumeClaimStatus{}
			s.create(t, pvc)
			if pvc.UID == oldUID {
				t.Fatal("replacement PVC retained UID")
			}
			s.diagnostic(t, name, "Deployment", "Error", "OwnershipConflict")
			s.get(t, pvcName, pvc)
			if pvc.UID == oldUID {
				t.Fatal("replacement UID not preserved")
			}
		} else {
			s.diagnostic(t, name, "Deployment", "Error", "StorageMissing")
			s.absent(t, pvcName, &corev1.PersistentVolumeClaim{})
		}
	}
}
