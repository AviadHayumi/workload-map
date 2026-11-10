// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	kartacel "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
)

type plan struct {
	Storage   *storageIntent
	Placement *placementIntent
	Service   *serviceIntent
}
type storageIntent struct{ Size string }
type placementIntent struct{ Pool string }
type serviceIntent struct{ Port, TargetPort int32 }
type programs struct{ storage, placement, service *kartacel.Program }
type diagnosticError struct{ reason, message string }

const (
	annotationStorage   = "plugins.karta.dev/storage"
	annotationPool      = "plugins.karta.dev/node-pool"
	annotationService   = "plugins.karta.dev/service-port"
	annotationState     = "plugins.karta.dev/state"
	annotationResult    = "plugins.karta.dev/result"
	poolKey             = "plugins.karta.dev/pool"
	workloadUIDKey      = "plugins.karta.dev/workload-uid"
	managedByKey        = "plugins.karta.dev/managed-by"
	parentAPIVersionKey = "plugins.karta.dev/parent-api-version"
	parentKindKey       = "plugins.karta.dev/parent-kind"
	parentNameKey       = "plugins.karta.dev/parent-name"
)

var supportedGVKs = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "batch", Version: "v1", Kind: "CronJob"},
}

//go:embed programs/*.cel
var sources embed.FS

func (e *diagnosticError) Error() string   { return e.reason + ": " + e.message }
func failure(reason, message string) error { return &diagnosticError{reason, message} }

func newPrograms() (*programs, error) {
	result := &programs{}
	for _, entry := range []struct {
		name   string
		target **kartacel.Program
	}{
		{"storage", &result.storage}, {"placement", &result.placement}, {"service", &result.service},
	} {
		source, err := sources.ReadFile("programs/" + entry.name + ".cel")
		if err != nil {
			return nil, fmt.Errorf("read %s program: %w", entry.name, err)
		}
		*entry.target, err = kartacel.Compile(string(source))
		if err != nil {
			return nil, fmt.Errorf("compile %s program: %w", entry.name, err)
		}
	}
	return result, nil
}

func (p *programs) plan(ctx context.Context, object *unstructured.Unstructured) (*plan, []string, error) {
	if object == nil {
		return nil, nil, failure("InvalidInput", "workload is nil")
	}
	supported := false
	for _, gvk := range supportedGVKs {
		if object.GroupVersionKind() == gvk {
			supported = true
		}
	}
	if !supported {
		return nil, nil, failure("UnsupportedWorkload", "only apps/v1 Deployment and batch/v1 CronJob are supported")
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	input, err := kartacel.NewInput(ctx, object)
	if err != nil {
		return nil, nil, failure("InvalidInput", "cannot create the root Pod template snapshot")
	}
	storage, err := p.storage.Evaluate(ctx, input)
	if err != nil {
		return nil, nil, failure("InvalidInput", "storage program evaluation failed")
	}
	placement, err := p.placement.Evaluate(ctx, input)
	if err != nil {
		return nil, nil, failure("InvalidInput", "placement program evaluation failed")
	}
	service, err := p.service.Evaluate(ctx, input)
	if err != nil {
		return nil, nil, failure("InvalidInput", "service program requires an integer service port and a first container port")
	}
	desired := &plan{}
	if desired.Storage, err = decodeStorage(storage); err != nil {
		return nil, nil, err
	}
	if desired.Placement, err = decodePlacement(placement); err != nil {
		return nil, nil, err
	}
	if desired.Service, err = decodeService(service); err != nil {
		return nil, nil, err
	}
	return desired, input.TemplatePath(), nil
}

func decodeStorage(value map[string]any) (*storageIntent, error) {
	if value == nil {
		return nil, failure("InvalidInput", "plugin result must be an object")
	}
	if len(value) == 0 {
		return nil, nil
	}
	size, ok := value["size"].(string)
	if len(value) != 1 || !ok || size == "" {
		return nil, failure("InvalidInput", "storage requires exactly one nonempty size string")
	}
	quantity, err := resource.ParseQuantity(size)
	limit := resource.MustParse("10Gi")
	if err != nil || quantity.Sign() <= 0 || quantity.Cmp(limit) > 0 {
		return nil, failure("InvalidInput", "storage size must be positive and at most 10Gi")
	}
	return &storageIntent{Size: quantity.String()}, nil
}

func decodePlacement(value map[string]any) (*placementIntent, error) {
	if value == nil {
		return nil, failure("InvalidInput", "plugin result must be an object")
	}
	if len(value) == 0 {
		return nil, nil
	}
	pool, ok := value["pool"].(string)
	if len(value) != 1 || !ok || pool == "" || len(validation.IsValidLabelValue(pool)) != 0 {
		return nil, failure("InvalidInput", "placement requires exactly one nonempty Kubernetes label value")
	}
	return &placementIntent{Pool: pool}, nil
}

func decodeService(value map[string]any) (*serviceIntent, error) {
	if value == nil {
		return nil, failure("InvalidInput", "plugin result must be an object")
	}
	if len(value) == 0 {
		return nil, nil
	}
	port, ok := value["port"].(int64)
	target, targetOK := value["targetPort"].(int64)
	if len(value) != 2 || !ok || !targetOK || port < 1 || port > 65535 || target < 1 || target > 65535 {
		return nil, failure("InvalidInput", "service requires exactly two integer ports in 1..65535")
	}
	return &serviceIntent{Port: int32(port), TargetPort: int32(target)}, nil
}

func inputHash(object *unstructured.Unstructured, storageClass string) string {
	annotations := object.GetAnnotations()
	values := map[string]any{"storageClass": storageClass, "targetPort": nil}
	for _, key := range []string{annotationStorage, annotationPool, annotationService} {
		value, present := annotations[key]
		values[key] = struct {
			Present bool
			Value   string
		}{present, value}
	}
	// This hash is only a diagnostic identity; child reconciliation never skips reads.
	path := []string{"spec", "template"}
	if object.GetKind() == "CronJob" {
		path = []string{"spec", "jobTemplate", "spec", "template"}
	}
	containers, found, _ := unstructured.NestedSlice(object.Object, append(path, "spec", "containers")...)
	if found && len(containers) > 0 {
		if container, ok := containers[0].(map[string]any); ok {
			ports, ok := container["ports"].([]any)
			if ok && len(ports) > 0 {
				if port, ok := ports[0].(map[string]any); ok {
					values["targetPort"] = port["containerPort"]
				}
			}
		}
	}
	encoded, _ := json.Marshal(values)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
