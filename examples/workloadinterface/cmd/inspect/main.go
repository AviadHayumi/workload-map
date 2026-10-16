// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Command inspect reads a Pod's workload through the public Karta SDK.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubediscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dsx-ai-factory/workload-map/pkg/attribution"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery"
	"github.com/dsx-ai-factory/workload-map/pkg/discovery/clientgo"
	"github.com/dsx-ai-factory/workload-map/pkg/karta"
)

type podSummary struct {
	Namespace string                   `json:"namespace"`
	Name      string                   `json:"name"`
	UID       types.UID                `json:"uid"`
	Phase     corev1.PodPhase          `json:"phase"`
	Component string                   `json:"component"`
	Path      []attribution.Coordinate `json:"path"`
	Issues    []string                 `json:"issues,omitempty"`
}

func main() {
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig path; empty uses client-go loading rules")
	kubeContext := flag.String("context", "", "kubeconfig context; empty uses its current context")
	namespace := flag.String("namespace", "default", "Pod namespace")
	name := flag.String("pod", "", "existing Pod name")
	maxDepth := flag.Int("max-depth", 0, "maximum controller-owner hops; zero uses 32")
	ownersOnly := flag.Bool("owners-only", false, "read only the Pod-to-root path; do not list Pods")
	flag.Parse()
	if *name == "" || *namespace == "" {
		fmt.Fprintln(os.Stderr, "-namespace and -pod must be non-empty")
		os.Exit(2)
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = *kubeconfig
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: *kubeContext}).ClientConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := inspect(ctx, config, *namespace, *name, *maxDepth, *ownersOnly); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func inspect(ctx context.Context, config *rest.Config, namespace, name string, maxDepth int, ownersOnly bool) error {
	config = rest.CopyConfig(config)
	config.Timeout = 15 * time.Second
	kube, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	apiDiscovery, err := kubediscovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(apiDiscovery))
	reader, err := clientgo.NewReader(kube, mapper)
	if err != nil {
		return err
	}
	sdk, err := karta.NewDiscoverer(reader, discovery.Options{MaxDepth: maxDepth})
	if err != nil {
		return err
	}

	object, err := kube.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).
		Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &pod); err != nil {
		return err
	}
	output := json.NewEncoder(os.Stdout)
	output.SetIndent("", "  ")
	if ownersOnly {
		path, err := sdk.OwnershipPath(ctx, discovery.PodReference(&pod))
		if err != nil {
			return err
		}
		return output.Encode(path)
	}
	observed, err := sdk.WorkloadForPod(ctx, discovery.PodReference(&pod))
	if err != nil {
		return err
	}
	memberships := observed.PodMemberships()
	pods := observed.Pods()
	summaries := make([]podSummary, 0, len(pods))
	for _, pod := range pods {
		membership := memberships[pod.UID]
		summary := podSummary{Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID,
			Phase: pod.Status.Phase, Component: membership.Component, Path: membership.Path}
		for _, issue := range membership.Issues {
			summary.Issues = append(summary.Issues, fmt.Sprintf("%s/%s: %v", issue.Component, issue.Axis, issue.Cause))
		}
		summaries = append(summaries, summary)
	}
	return output.Encode(struct {
		Workload discovery.ObjectRef `json:"workload"`
		Generic  bool                `json:"generic"`
		Pods     []podSummary        `json:"pods"`
	}{Workload: observed.Root(), Generic: observed.Generic(), Pods: summaries})
}
