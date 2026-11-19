// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
	"github.com/dsx-ai-factory/workload-map/operator/pkg/cataloginstall"
)

var installCatalog bool
var catalogOwner, catalogWebhook string

func init() {
	flag.BoolVar(&installCatalog, "install-catalog", false, "Install the bundled CEL catalog and exit.")
	flag.StringVar(&catalogOwner, "catalog-owner", "", "Helm namespace/release owning installed catalog definitions.")
	flag.StringVar(&catalogWebhook, "catalog-webhook", "", "Validating webhook configuration to await before installation.")
}

func runCatalogInstall(ctx context.Context, config *rest.Config) error {
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	definitions := catalog.List()
	if len(definitions) == 0 {
		return fmt.Errorf("catalog is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := cataloginstall.CheckConflicts(ctx, c, catalogOwner, definitions); err != nil {
		return err
	}
	if err := cataloginstall.WaitReady(ctx, c, catalogWebhook, definitions[0]); err != nil {
		return fmt.Errorf("catalog admission readiness: %w", err)
	}
	return cataloginstall.Install(ctx, c, catalogOwner, definitions)
}
