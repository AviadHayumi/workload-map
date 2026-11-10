// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type pvcClient struct {
	client.Client
	restClient rest.Interface
}

func newPVCClient(base client.Client, config *rest.Config) (*pvcClient, error) {
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	// HTTPClientFor can return the shared default client. Keep redirect policy local.
	singleAttempt := *httpClient
	singleAttempt.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	core, err := coreclient.NewForConfigAndClient(config, &singleAttempt)
	if err != nil {
		return nil, err
	}
	return &pvcClient{Client: base, restClient: core.RESTClient()}, nil
}

func (c *pvcClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	claim, ok := object.(*corev1.PersistentVolumeClaim)
	if !ok {
		return c.Client.Create(ctx, object, options...)
	}
	createOptions := (&client.CreateOptions{}).ApplyOptions(options)
	// A lost response consumes the persisted reservation. Neither client-go nor
	// redirects may replay the storage POST before its UID is committed.
	return c.restClient.Post().Namespace(claim.Namespace).Resource("persistentvolumeclaims").
		VersionedParams(createOptions.AsCreateOptions(), clientgoscheme.ParameterCodec).
		Body(claim).MaxRetries(0).Do(ctx).Into(claim)
}
