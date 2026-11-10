// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func main() {
	var namespace, kubeconfig, storageClass string
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&namespace, "namespace", "", "required namespace to watch")
	flags.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig path; otherwise standard Kubernetes configuration")
	flags.StringVar(&storageClass, "storage-class", "", "PVC storage class; empty explicitly requests no class")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}
	ctrl.SetLogger(zap.New())
	if err := run(namespace, kubeconfig, storageClass); err != nil {
		ctrl.Log.Error(err, "controller stopped")
		os.Exit(1)
	}
}

func run(namespace, kubeconfig, storageClass string) error {
	if namespace == "" || len(validation.IsDNS1123Label(namespace)) != 0 {
		return fmt.Errorf("--namespace must be a nonempty Kubernetes namespace")
	}
	if storageClass != "" && len(validation.IsDNS1123Subdomain(storageClass)) != 0 {
		return fmt.Errorf("--storage-class must be a Kubernetes storage class name")
	}
	programs, err := newPrograms()
	if err != nil {
		return err
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	manager, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: ":8081", LeaderElection: false,
	})
	if err != nil {
		return err
	}
	workloadClient, err := newPVCClient(manager.GetClient(), config)
	if err != nil {
		return err
	}
	watchObjects := []client.Object{&corev1.Service{}, &corev1.PersistentVolumeClaim{}}
	for _, gvk := range supportedGVKs {
		r := &reconciler{client: workloadClient, reader: manager.GetAPIReader(), namespace: namespace, storageClass: storageClass, gvk: gvk, programs: programs, recorder: manager.GetEventRecorder("cel-plugins")}
		parent := &unstructured.Unstructured{}
		parent.SetGroupVersionKind(gvk)
		watchObjects = append(watchObjects, parent)
		mapper := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []reconcile.Request { return childRequests(object, gvk) })
		if err := ctrl.NewControllerManagedBy(manager).
			Named("cel-plugins-"+gvk.Kind).
			For(parent, builder.WithPredicates(parentPredicate())).
			Watches(&corev1.Service{}, mapper).
			Watches(&corev1.PersistentVolumeClaim{}, mapper).
			Complete(r); err != nil {
			return err
		}
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	informers := make([]cache.Informer, 0, len(watchObjects))
	for _, object := range watchObjects {
		informer, err := manager.GetCache().GetInformer(context.Background(), object, cache.BlockUntilSynced(false))
		if err != nil {
			return err
		}
		informers = append(informers, informer)
	}
	if err := manager.AddReadyzCheck("readyz", func(_ *http.Request) error {
		for _, informer := range informers {
			if !informer.HasSynced() {
				return fmt.Errorf("workload and child caches have not synced")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return manager.Start(ctrl.SetupSignalHandler())
}

func childRequests(object client.Object, gvk schema.GroupVersionKind) []reconcile.Request {
	annotations, labels := object.GetAnnotations(), object.GetLabels()
	if object.GetNamespace() == "" || labels[managedByKey] != "cel-plugins" || labels[workloadUIDKey] == "" ||
		annotations[parentAPIVersionKey] != gvk.GroupVersion().String() || annotations[parentKindKey] != gvk.Kind ||
		annotations[parentNameKey] == "" || len(validation.IsDNS1123Subdomain(annotations[parentNameKey])) != 0 {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: object.GetNamespace(), Name: annotations[parentNameKey]}}}
}

func parentPredicate() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		oldAnnotations, newAnnotations := map[string]string{}, map[string]string{}
		for key, value := range e.ObjectOld.GetAnnotations() {
			if key != annotationResult {
				oldAnnotations[key] = value
			}
		}
		for key, value := range e.ObjectNew.GetAnnotations() {
			if key != annotationResult {
				newAnnotations[key] = value
			}
		}
		if !reflect.DeepEqual(oldAnnotations, newAnnotations) || !reflect.DeepEqual(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels()) ||
			e.ObjectOld.GetUID() != e.ObjectNew.GetUID() || !reflect.DeepEqual(e.ObjectOld.GetDeletionTimestamp(), e.ObjectNew.GetDeletionTimestamp()) {
			return true
		}
		oldObject, oldOK := e.ObjectOld.(*unstructured.Unstructured)
		newObject, newOK := e.ObjectNew.(*unstructured.Unstructured)
		if oldOK && newOK {
			return !reflect.DeepEqual(oldObject.Object["spec"], newObject.Object["spec"])
		}
		return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
	}}
}
