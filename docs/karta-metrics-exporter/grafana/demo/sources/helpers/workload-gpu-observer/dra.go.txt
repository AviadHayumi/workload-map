// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type draInventory struct {
	claims  map[string]resourcev1.ResourceClaim
	devices map[string]resourcev1.Device
}

type draSample struct{ claim, namespace, driver, pool, device, migUUID, profile string }
type draAllocation struct{ claimUID, uuid, parent, profile, resource string }

func (d draSample) key() string {
	return d.namespace + "/" + d.claim + "/" + d.driver + "/" + d.pool + "/" + d.device
}

func readDRA(ctx context.Context, kube kubernetes.Interface, cfg config, pods []corev1.Pod) (*draInventory, error) {
	needed := false
	for _, p := range pods {
		if len(p.Spec.ResourceClaims) > 0 && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			needed = true
		}
	}
	if !cfg.nativeDCGM || !needed {
		return nil, nil
	}
	var claims []resourcev1.ResourceClaim
	var slices []resourcev1.ResourceSlice
	continuation := ""
	for {
		list, err := kube.ResourceV1().ResourceClaims(cfg.namespace).List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return nil, fmt.Errorf("list ResourceClaims: %w", err)
		}
		claims = append(claims, list.Items...)
		if len(claims) > maxObjects {
			return nil, errors.New("claim inventory limit exceeded")
		}
		continuation = list.Continue
		if continuation == "" {
			break
		}
	}
	for {
		list, err := kube.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{Limit: 500, Continue: continuation})
		if err != nil {
			return nil, fmt.Errorf("list ResourceSlices: %w", err)
		}
		slices = append(slices, list.Items...)
		if len(slices) > maxObjects {
			return nil, errors.New("slice inventory limit exceeded")
		}
		continuation = list.Continue
		if continuation == "" {
			break
		}
	}
	return indexDRA(cfg, claims, slices)
}

func indexDRA(cfg config, claims []resourcev1.ResourceClaim, slices []resourcev1.ResourceSlice) (*draInventory, error) {
	i := &draInventory{claims: make(map[string]resourcev1.ResourceClaim), devices: make(map[string]resourcev1.Device)}
	for _, c := range claims {
		if c.Namespace != cfg.namespace {
			return nil, errors.New("claim outside selected namespace")
		}
		if _, exists := i.claims[c.Name]; exists {
			return nil, errors.New("duplicate claim")
		}
		i.claims[c.Name] = c
	}
	var pool *resourcev1.ResourcePool
	count := int64(0)
	for _, s := range slices {
		if s.Spec.Driver != "gpu.nvidia.com" || s.Spec.NodeName == nil || *s.Spec.NodeName != cfg.node {
			continue
		}
		if s.DeletionTimestamp != nil || s.Spec.Pool.Name != cfg.node {
			return nil, errors.New("unsupported DRA pool")
		}
		if pool == nil {
			p := s.Spec.Pool
			pool = &p
		}
		if *pool != s.Spec.Pool {
			return nil, errors.New("inconsistent DRA pool generation")
		}
		count++
		for _, d := range s.Spec.Devices {
			key := s.Spec.Driver + "/" + s.Spec.Pool.Name + "/" + d.Name
			if _, exists := i.devices[key]; exists {
				return nil, errors.New("duplicate DRA device")
			}
			i.devices[key] = d
			if len(i.devices) > 1024 {
				return nil, errors.New("DRA device inventory limit exceeded")
			}
		}
	}
	if pool == nil || count != pool.ResourceSliceCount {
		return nil, errors.New("incomplete DRA pool")
	}
	return i, nil
}

func requestsForDRA(r gpuRequest, inventory *draInventory) gpuRequest {
	r.unsupported = true
	if inventory == nil || len(r.counts) != 0 {
		return r
	}
	for _, c := range r.pod.Spec.InitContainers {
		if len(c.Resources.Claims) > 0 {
			return r
		}
	}
	aliases := make(map[string]string)
	for _, ref := range r.pod.Spec.ResourceClaims {
		name := ""
		if ref.ResourceClaimName != nil {
			name = *ref.ResourceClaimName
		}
		if ref.ResourceClaimTemplateName != nil {
			for _, status := range r.pod.Status.ResourceClaimStatuses {
				if status.Name == ref.Name && status.ResourceClaimName != nil {
					name = *status.ResourceClaimName
				}
			}
		}
		if name == "" || aliases[ref.Name] != "" {
			return r
		}
		aliases[ref.Name] = name
	}
	r.dra = make(map[string]map[string]draAllocation)
	used := make(map[string]bool)
	for _, c := range r.pod.Spec.Containers {
		for _, ref := range c.Resources.Claims {
			name := aliases[ref.Name]
			claim, ok := inventory.claims[name]
			if !ok || name == "" || used[name] || ref.Request != "" || claim.UID == "" || claim.DeletionTimestamp != nil || claim.Status.Allocation == nil || len(claim.Spec.Devices.Requests) != 1 || len(claim.Spec.Devices.Config) != 0 || len(claim.Status.Allocation.Devices.Config) != 0 || len(claim.Status.ReservedFor) != 1 {
				return r
			}
			consumer := claim.Status.ReservedFor[0]
			if consumer.APIGroup != "" || consumer.Resource != "pods" || consumer.Name != r.pod.Name || consumer.UID != r.pod.UID {
				return r
			}
			req := claim.Spec.Devices.Requests[0]
			exact := req.Exactly
			if exact == nil || len(req.FirstAvailable) != 0 || (exact.AllocationMode != "" && exact.AllocationMode != resourcev1.DeviceAllocationModeExactCount) || (exact.AdminAccess != nil && *exact.AdminAccess) || exact.Capacity != nil {
				return r
			}
			n := exact.Count
			if n == 0 {
				n = 1
			}
			results := claim.Status.Allocation.Devices.Results
			if n < 1 || n > 1024 || int64(len(results)) != n {
				return r
			}
			if r.dra[c.Name] == nil {
				r.dra[c.Name] = make(map[string]draAllocation)
			}
			for _, result := range results {
				if result.Request != req.Name || result.Driver != "gpu.nvidia.com" || result.Pool != r.pod.Spec.NodeName || result.ShareID != nil || len(result.ConsumedCapacity) != 0 || (result.AdminAccess != nil && *result.AdminAccess) || len(result.BindingConditions) != 0 || len(result.BindingFailureConditions) != 0 {
					return r
				}
				device, ok := inventory.devices[result.Driver+"/"+result.Pool+"/"+result.Device]
				if !ok || (device.AllowMultipleAllocations != nil && *device.AllowMultipleAllocations) {
					return r
				}
				attr := func(name string) string {
					v := device.Attributes[resourcev1.QualifiedName(name)]
					if v.StringValue == nil {
						return ""
					}
					return *v.StringValue
				}
				a := draAllocation{claimUID: string(claim.UID), uuid: attr("uuid"), parent: attr("parentUUID"), profile: attr("profile")}
				switch {
				case exact.DeviceClassName == "gpu.nvidia.com" && attr("type") == "gpu" && strings.HasPrefix(a.uuid, "GPU-") && a.profile == "" && a.parent == "":
					a.resource = "nvidia.com/gpu"
				case exact.DeviceClassName == "mig.nvidia.com" && attr("type") == "mig" && strings.HasPrefix(a.uuid, "MIG-") && strings.HasPrefix(a.parent, "GPU-") && migProfile.MatchString(a.profile):
					a.resource = "nvidia.com/mig-" + a.profile
				default:
					return r
				}
				if r.resources[c.Name] != "" && r.resources[c.Name] != a.resource {
					return r
				}
				r.resources[c.Name] = a.resource
				key := (draSample{namespace: claim.Namespace, claim: name, driver: result.Driver, pool: result.Pool, device: result.Device}).key()
				if _, exists := r.dra[c.Name][key]; exists {
					return r
				}
				r.dra[c.Name][key] = a
				r.counts[c.Name]++
			}
			used[name] = true
		}
	}
	if len(used) != len(aliases) || len(used) == 0 {
		return r
	}
	r.unsupported = false
	return r
}

func matchesDRA(r gpuRequest, s gpuSample) bool {
	if len(r.dra) == 0 {
		return s.dra.claim == "" && s.dra.driver == "" && s.dra.device == ""
	}
	a, ok := r.dra[s.container][s.dra.key()]
	if !ok || a.resource != s.resource {
		return false
	}
	if a.profile == "" {
		return a.uuid == s.uuid && s.dra.migUUID == "" && s.dra.profile == ""
	}
	return a.uuid == s.dra.migUUID && a.profile == s.dra.profile && strings.HasPrefix(s.uuid, a.parent+"/gi/")
}
