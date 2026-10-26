// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type runner struct {
	kube                                kubernetes.Interface
	dynamic                             dynamic.Interface
	namespace, configMap, holder, image string
	now                                 func() time.Time
}

type workloadType struct {
	resource     schema.GroupVersionResource
	kind, prefix string
}

type root struct {
	object   unstructured.Unstructured
	workload workloadType
}

const (
	ownerLabel           = "karta.demo/finite-submitter"
	slotAnnotation       = "karta.demo/finite-slot"
	leaseHolder          = "karta.demo/lease-holder"
	leaseUntil           = "karta.demo/lease-until"
	cadence              = 5 * time.Minute
	retention            = 45 * time.Minute
	leaseDuration        = 2 * time.Minute
	maxRoots             = 10
	rootDeadline   int64 = 300
	runSeconds     int64 = 90
)

var workloadTypes = []workloadType{
	{schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}, "JobSet", "finite-jobset-"},
	{schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1", Resource: "pytorchjobs"}, "PyTorchJob", "finite-pytorch-"},
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	r := runner{now: time.Now}
	flag.StringVar(&r.namespace, "namespace", "", "Dedicated application namespace")
	flag.StringVar(&r.configMap, "state-configmap", "finite-workload-submitter", "Precreated ownership and lease ConfigMap")
	flag.StringVar(&r.image, "workload-image", "busybox:1.36@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662", "Pinned CPU fixture image")
	flag.Parse()
	r.holder = os.Getenv("POD_UID")
	if r.namespace == "" || r.configMap == "" || r.holder == "" || r.image == "" {
		return errors.New("namespace, state-configmap, workload-image and POD_UID are required")
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	cfg.Timeout = 10 * time.Second
	r.kube, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	r.dynamic, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return r.tick(ctx)
}

func (r *runner) tick(ctx context.Context) error {
	cm, err := r.kube.CoreV1().ConfigMaps(r.namespace).Get(ctx, r.configMap, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cm.UID == "" {
		return errors.New("ownership ConfigMap UID is missing")
	}
	if until := cm.Annotations[leaseUntil]; until != "" {
		expiry, parseErr := time.Parse(time.RFC3339Nano, until)
		if parseErr != nil {
			return fmt.Errorf("invalid lease expiry: %w", parseErr)
		}
		if r.now().Before(expiry) {
			log.Print("another submitter invocation holds the lease; skipped")
			return nil
		}
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[leaseHolder] = r.holder
	cm.Annotations[leaseUntil] = r.now().Add(leaseDuration).UTC().Format(time.RFC3339Nano)
	cm, err = r.kube.CoreV1().ConfigMaps(r.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("acquire lease: %w", err)
	}
	// The process deadline is shorter than the lease. A crashed process cannot
	// overlap a successor; deterministic slot names also make retries idempotent.
	result, workErr := r.reconcile(ctx, cm)
	cm.Data = map[string]string{"last-result": result, "last-run": r.now().UTC().Format(time.RFC3339Nano)}
	if workErr != nil {
		cm.Data["last-error"] = workErr.Error()
	}
	delete(cm.Annotations, leaseHolder)
	delete(cm.Annotations, leaseUntil)
	_, releaseErr := r.kube.CoreV1().ConfigMaps(r.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	return errors.Join(workErr, releaseErr)
}

func (r *runner) reconcile(ctx context.Context, cm *corev1.ConfigMap) (string, error) {
	var roots []root
	for _, w := range workloadTypes {
		list, err := r.dynamic.Resource(w.resource).Namespace(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: ownerLabel + "=" + string(cm.UID)})
		if err != nil {
			return "list failed", err
		}
		for _, obj := range list.Items {
			if !owned(&obj, w, cm) {
				return "ownership conflict", fmt.Errorf("refusing inconsistent ownership on %s/%s", w.kind, obj.GetName())
			}
			roots = append(roots, root{obj, w})
		}
	}
	count, active := len(roots), 0
	for _, item := range roots {
		terminal, finished := completion(&item.object)
		if !terminal || item.object.GetDeletionTimestamp() != nil {
			active++
			continue
		}
		if finished.IsZero() || r.now().Sub(finished) < retention {
			continue
		}
		api := r.dynamic.Resource(item.workload.resource).Namespace(r.namespace)
		current, err := api.Get(ctx, item.object.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			count--
			continue
		}
		if err != nil {
			return "cleanup read failed", err
		}
		isTerminal, ended := completion(current)
		if current.GetUID() != item.object.GetUID() || !owned(current, item.workload, cm) || !isTerminal || ended.IsZero() || r.now().Sub(ended) < retention {
			return "cleanup changed", errors.New("cleanup target identity or terminal state changed")
		}
		uid, rv := current.GetUID(), current.GetResourceVersion()
		propagation := metav1.DeletePropagationForeground
		err = api.Delete(ctx, current.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}, PropagationPolicy: &propagation})
		if err != nil && !apierrors.IsNotFound(err) {
			return "cleanup failed", err
		}
		// Wait for a later tick to observe garbage collection. The hard count cap
		// includes objects whose deletion has only been requested.
		log.Printf("cleanup requested kind=%s name=%s uid=%s", item.workload.kind, current.GetName(), uid)
	}
	if active > 0 {
		return fmt.Sprintf("skipped: %d active or deleting roots", active), nil
	}
	if count >= maxRoots {
		return fmt.Sprintf("skipped: retained root cap %d", maxRoots), nil
	}
	slot := r.now().Unix() / int64(cadence/time.Second)
	w := workloadTypes[slot%int64(len(workloadTypes))]
	obj := r.fixture(w, slot, cm)
	api := r.dynamic.Resource(w.resource).Namespace(r.namespace)
	created, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := api.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if getErr != nil {
			return "retry read failed", getErr
		}
		if !owned(existing, w, cm) {
			return "name conflict", errors.New("scheduled name exists with different ownership")
		}
		return "already submitted " + w.kind + "/" + existing.GetName(), nil
	}
	if err != nil {
		return "create failed", err
	}
	result := fmt.Sprintf("submitted %s/%s uid=%s", w.kind, created.GetName(), created.GetUID())
	log.Print(result)
	return result, nil
}

func owned(obj *unstructured.Unstructured, w workloadType, cm *corev1.ConfigMap) bool {
	slot, err := strconv.ParseInt(obj.GetAnnotations()[slotAnnotation], 10, 64)
	if err != nil || slot < 0 || obj.GetNamespace() != cm.Namespace || obj.GetName() != w.prefix+strconv.FormatInt(slot, 10) || obj.GetLabels()[ownerLabel] != string(cm.UID) {
		return false
	}
	for _, owner := range obj.GetOwnerReferences() {
		if owner.APIVersion == "v1" && owner.Kind == "ConfigMap" && owner.Name == cm.Name && owner.UID == cm.UID {
			return true
		}
	}
	return false
}

func completion(obj *unstructured.Unstructured) (bool, time.Time) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if !ok || c["status"] != "True" {
			continue
		}
		switch c["type"] {
		case "Completed", "Succeeded", "Failed":
			timestamp, _ := c["lastTransitionTime"].(string)
			finished, _ := time.Parse(time.RFC3339Nano, timestamp)
			return true, finished
		}
	}
	return false, time.Time{}
}

func (r *runner) fixture(w workloadType, slot int64, cm *corev1.ConfigMap) *unstructured.Unstructured {
	labels := map[string]any{ownerLabel: string(cm.UID), "app.kubernetes.io/managed-by": "finite-workload-submitter"}
	// Bounded CPU work produces real execution and exit status, not fabricated
	// training results. Sleep keeps the Running phase visible between scrapes.
	command := fmt.Sprintf("echo 'finite CPU fixture started'; i=0; while [ $i -lt %d ]; do printf 'sample-%%s\\n' \"$i\" | sha256sum >/dev/null; sleep 1; i=$((i+1)); done; echo 'finite CPU fixture completed'", runSeconds)
	pod := func(container string) map[string]any {
		return map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{
			"restartPolicy": "Never", "automountServiceAccountToken": false, "terminationGracePeriodSeconds": int64(5),
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(65532), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers": []any{map[string]any{"name": container, "image": r.image, "command": []any{"sh", "-ec", command},
				"resources":       map[string]any{"requests": map[string]any{"cpu": "50m", "memory": "64Mi"}, "limits": map[string]any{"cpu": "200m", "memory": "128Mi"}},
				"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}},
		}}
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": w.resource.GroupVersion().String(), "kind": w.kind, "metadata": map[string]any{"name": w.prefix + strconv.FormatInt(slot, 10), "namespace": r.namespace, "labels": labels, "annotations": map[string]any{slotAnnotation: strconv.FormatInt(slot, 10)}}}}
	obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: cm.Name, UID: types.UID(cm.UID)}})
	switch w.kind {
	case "JobSet":
		jobs := []any{}
		for _, role := range []string{"chief", "worker"} {
			jobs = append(jobs, map[string]any{"name": role, "replicas": int64(1), "template": map[string]any{"spec": map[string]any{"parallelism": int64(1), "completions": int64(1), "backoffLimit": int64(0), "activeDeadlineSeconds": rootDeadline, "template": pod("worker")}}})
		}
		obj.Object["spec"] = map[string]any{"failurePolicy": map[string]any{"maxRestarts": int64(0)}, "replicatedJobs": jobs}
	case "PyTorchJob":
		replicas := map[string]any{}
		for _, role := range []string{"Master", "Worker"} {
			replicas[role] = map[string]any{"replicas": int64(1), "restartPolicy": "Never", "template": pod("pytorch")}
		}
		obj.Object["spec"] = map[string]any{"runPolicy": map[string]any{"cleanPodPolicy": "None", "activeDeadlineSeconds": rootDeadline, "backoffLimit": int64(0)}, "pytorchReplicaSpecs": replicas}
	}
	return obj
}
