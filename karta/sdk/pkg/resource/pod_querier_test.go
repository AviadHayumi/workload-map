// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha2"
)

var _ = Describe("PodQuerier", func() {
	var (
		ctx     context.Context
		testPod corev1.Pod
		querier *PodQuerier
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create a test pod with labels and annotations
		testPod = corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: "default",
				Labels: map[string]string{
					"component":   "worker",
					"app":         "pytorch",
					"version":     "v1.0",
					"environment": "production",
				},
				Annotations: map[string]string{
					"config": "high-memory",
					"owner":  "team-ai",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "main",
						Image: "pytorch:latest",
					},
				},
			},
		}

		querier = NewPodQuerier(&testPod)
	})
	Describe("MatchesComponentType", func() {
		Context("when selector is nil", func() {
			It("should return false", func() {
				matches, err := querier.MatchesComponentType(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeFalse())
			})
		})

		Context("when checking key existence (Value is nil)", func() {
			It("should return true for existing label keys", func() {
				selector := &v1alpha1.ComponentTypeSelector{
					Value: nil, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"component"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should return false for non-existing label keys", func() {
				selector := &v1alpha1.ComponentTypeSelector{
					Value: nil, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"nonexistent"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeFalse())
			})

			It("should return true for existing annotation keys", func() {
				selector := &v1alpha1.ComponentTypeSelector{
					Value: nil, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"annotations"][?"config"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should return true for existing nested paths", func() {
				selector := &v1alpha1.ComponentTypeSelector{
					Value: nil, Read: v1alpha1.ReadAccessor{Expression: `object.spec.containers[0].name`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})
		})

		Context("when checking key-value pairs (Value is specified)", func() {
			It("should return true for matching label values", func() {
				value := "worker"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"component"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should return false for non-matching label values", func() {
				value := "master"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"component"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeFalse())
			})

			It("should return true for matching annotation values", func() {
				value := "high-memory"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"annotations"][?"config"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should return false for non-matching annotation values", func() {
				value := "low-memory"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"annotations"][?"config"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeFalse())
			})

			It("should handle special characters in values", func() {
				// Update the test pod to have a label with special characters
				testPod.Labels["special"] = "value-with-special_chars.and:colons"
				querier = NewPodQuerier(&testPod)

				value := "value-with-special_chars.and:colons"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"special"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should handle values with quotes", func() {
				// Update the test pod to have a label with quotes
				testPod.Labels["quotes"] = `value-with-"quotes"`
				querier = NewPodQuerier(&testPod)

				value := `value-with-"quotes"`
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"quotes"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should return false for non-existing keys with values", func() {
				value := "any-value"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"nonexistent"].orValue(null)`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeFalse())
			})
		})

		Context("when using complex expressions", func() {
			It("should work with array indexing", func() {
				value := "main"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object.spec.containers[0].name`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})

			It("should work with object navigation", func() {
				value := "default"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: `object.metadata.namespace`}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(matches).To(BeTrue())
			})
		})

		Context("when the expression is invalid", func() {
			It("should return an error for invalid expressions", func() {
				value := "any"
				selector := &v1alpha1.ComponentTypeSelector{
					Value: &value, Read: v1alpha1.ReadAccessor{Expression: "object..invalid[[["}}

				matches, err := querier.MatchesComponentType(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(matches).To(BeFalse())
			})
		})
	})

	Describe("ExtractReplicaKey", func() {
		Context("when selector is nil", func() {
			It("should return empty string and found=false", func() {
				key, found, err := querier.ExtractReplicaKey(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(key).To(Equal(""))
			})
		})

		Context("with valid selector", func() {
			It("should extract replica key from label", func() {
				testPod.Labels["group-index"] = "2"
				querier = NewPodQuerier(&testPod)

				selector := &v1alpha1.ReplicaSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.labels["group-index"]`}}

				key, found, err := querier.ExtractReplicaKey(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(key).To(Equal("2"))
			})

			It("should extract replica key from annotation", func() {
				testPod.Annotations["replica-id"] = "group-0"
				querier = NewPodQuerier(&testPod)

				selector := &v1alpha1.ReplicaSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.annotations["replica-id"]`}}

				key, found, err := querier.ExtractReplicaKey(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(key).To(Equal("group-0"))
			})
		})

		Context("with invalid selector", func() {
			It("should return error for non-existent path", func() {
				selector := &v1alpha1.ReplicaSelector{Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"nonexistent"].orValue(null)`}}

				key, found, err := querier.ExtractReplicaKey(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("query result is empty"))
				Expect(found).To(BeFalse())
				Expect(key).To(Equal(""))
			})

			It("should return error for an invalid expression", func() {
				selector := &v1alpha1.ReplicaSelector{Read: v1alpha1.ReadAccessor{Expression: "object..invalid[[["}}

				key, found, err := querier.ExtractReplicaKey(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(key).To(Equal(""))
			})

			It("should return error for path returning multiple values", func() {
				selector := &v1alpha1.ReplicaSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.labels.map(k, object.metadata.labels[k])`}}

				key, found, err := querier.ExtractReplicaKey(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("expected single query result"))
				Expect(found).To(BeFalse())
				Expect(key).To(Equal(""))
			})
		})
	})

	Describe("ExtractInstanceId", func() {
		Context("when selector is nil", func() {
			It("should return empty string and found=false", func() {
				id, found, err := querier.ExtractInstanceId(ctx, nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(id).To(Equal(""))
			})
		})

		Context("when the expression is empty", func() {
			It("should return empty string and found=false", func() {
				selector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: ""}}

				id, found, err := querier.ExtractInstanceId(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(id).To(Equal(""))
			})
		})

		Context("with valid selector", func() {
			It("should extract instance id from label", func() {
				testPod.Labels["job-name"] = "indexer"
				querier = NewPodQuerier(&testPod)

				selector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.labels["job-name"]`}}

				id, found, err := querier.ExtractInstanceId(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(id).To(Equal("indexer"))
			})

			It("should extract instance id from annotation", func() {
				selector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.annotations.config`}}

				id, found, err := querier.ExtractInstanceId(ctx, selector)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(id).To(Equal("high-memory"))
			})
		})

		Context("with invalid selector", func() {
			It("should return error for non-existent path", func() {
				selector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"nonexistent"].orValue(null)`}}

				id, found, err := querier.ExtractInstanceId(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("query result is empty"))
				Expect(found).To(BeFalse())
				Expect(id).To(Equal(""))
			})

			It("should return error for an invalid expression", func() {
				selector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: "object..invalid[[["}}

				id, found, err := querier.ExtractInstanceId(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(id).To(Equal(""))
			})

			It("should return error for path returning multiple values", func() {
				selector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.labels.map(k, object.metadata.labels[k])`}}

				id, found, err := querier.ExtractInstanceId(ctx, selector)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("expected single query result"))
				Expect(found).To(BeFalse())
				Expect(id).To(Equal(""))
			})
		})
	})

	Describe("GetMatchingInstanceId", func() {
		var pod *corev1.Pod

		BeforeEach(func() {
			pod = &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Labels: map[string]string{
						"job-name":     "indexer",
						"service-name": "api",
						"component":    "worker",
					},
					Annotations: map[string]string{
						"nvidia.com/dynamo-component": "worker-group-1",
					},
				},
			}
		})

		Context("with valid instance selector", func() {
			It("should extract instance ID from pod label", func() {
				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.labels["job-name"]`}}
				instanceIds := []string{"indexer", "processor"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("indexer"))
			})

			It("should extract instance ID from pod annotation", func() {
				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.annotations["nvidia.com/dynamo-component"]`}}
				instanceIds := []string{"worker-group-1", "worker-group-2"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("worker-group-1"))
			})

			It("should extract instance ID from nested pod spec field", func() {
				pod.Spec = corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "worker",
							Env: []corev1.EnvVar{
								{Name: "GROUP_NAME", Value: "cache"},
							},
						},
					},
				}

				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.spec.containers[0].env.filter(e, e.name == "GROUP_NAME").map(e, e.value)`}}
				instanceIds := []string{"api", "worker", "cache"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("cache"))
			})
		})

		Context("with invalid instance selector", func() {
			It("should return error when the expression is invalid", func() {
				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: "object..invalid[[["}}
				instanceIds := []string{"indexer", "processor"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))
				Expect(err.Error()).To(ContainSubstring("failed to extract instance id"))
			})

			It("should return error when the expression returns multiple results", func() {
				pod.Labels["duplicate-key"] = "value1"
				pod.Annotations["duplicate-key"] = "value2"

				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `[object.metadata.labels["duplicate-key"], object.metadata.annotations["duplicate-key"]]`}}
				instanceIds := []string{"value1", "value2"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))
				Expect(err.Error()).To(ContainSubstring("expected single query result"))
			})

			It("should return error when the expression yields nothing", func() {
				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"nonexistent"].orValue(null)`}}
				instanceIds := []string{"indexer", "processor"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))
				Expect(err.Error()).To(ContainSubstring("query result is empty"))
			})
		})

		Context("instance ID validation", func() {
			It("should return InstanceNotFoundError when extracted value not in instance IDs list", func() {
				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `object.metadata.labels["job-name"]`}}
				instanceIds := []string{"processor", "validator"} // "indexer" not in list

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))

				var instanceNotFoundErr InstanceNotFoundError
				Expect(errors.As(err, &instanceNotFoundErr)).To(BeTrue())
				Expect(string(instanceNotFoundErr)).To(ContainSubstring("could not match instance id"))
			})

			It("should handle numeric values by converting to string", func() {
				pod.Labels["replica-id"] = "3"

				querier := NewPodQuerier(pod)
				instanceSelector := &v1alpha1.ComponentInstanceSelector{Read: v1alpha1.ReadAccessor{Expression: `int(object.metadata.labels["replica-id"])`}}
				instanceIds := []string{"1", "2", "3", "4"}

				result, err := querier.GetMatchingInstanceId(ctx, instanceSelector, instanceIds)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal("3"))
			})
		})

		Context("without instance selector", func() {
			It("should match single instance with empty ID when no selector provided", func() {
				querier := NewPodQuerier(pod)
				instanceIds := []string{""} // Single instance with empty ID

				result, err := querier.GetMatchingInstanceId(ctx, nil, instanceIds)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(""))
			})

			It("should return error when no selector provided but multiple instance IDs exist", func() {
				querier := NewPodQuerier(pod)
				instanceIds := []string{"worker-1", "worker-2"} // Multiple instances

				result, err := querier.GetMatchingInstanceId(ctx, nil, instanceIds)

				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))
				Expect(err.Error()).To(ContainSubstring("no instance selector provided but instance ids are not empty"))
			})

			It("should return error when no selector provided with single non-empty instance ID", func() {
				querier := NewPodQuerier(pod)
				instanceIds := []string{"worker-1"} // Single non-empty instance

				result, err := querier.GetMatchingInstanceId(ctx, nil, instanceIds)

				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(""))
				Expect(err.Error()).To(ContainSubstring("no instance selector provided but instance ids are not empty"))
			})
		})
	})
})

var _ = Describe("MatchesComponentType with a CEL expression", func() {
	ctx := context.Background()
	// The catalog spelling: an optional chain ending in orValue(null). The value comparison must
	// not be built as source, because the checker types the chain as null and rejects `== "v"`.
	selector := func(value string) *v1alpha1.ComponentTypeSelector {
		return &v1alpha1.ComponentTypeSelector{
			Value: ptr.To(value), Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"role"].orValue(null)`}}
	}
	podWith := func(labels map[string]string) *PodQuerier {
		return NewPodQuerier(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Labels: labels}})
	}

	It("should match the pod whose label equals the value", func() {
		matches, err := podWith(map[string]string{"role": "worker"}).MatchesComponentType(ctx, selector("worker"))
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeTrue())
	})

	It("should not match a different value", func() {
		matches, err := podWith(map[string]string{"role": "leader"}).MatchesComponentType(ctx, selector("worker"))
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeFalse())
	})

	It("should not match when the label is missing", func() {
		matches, err := podWith(nil).MatchesComponentType(ctx, selector("worker"))
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeFalse())
	})

	It("should check existence through the expression when no value is set", func() {
		sel := &v1alpha1.ComponentTypeSelector{Read: v1alpha1.ReadAccessor{Expression: `object[?"metadata"][?"labels"][?"role"].orValue(null)`}}
		matches, err := podWith(map[string]string{"role": "worker"}).MatchesComponentType(ctx, sel)
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeTrue())

		matches, err = podWith(nil).MatchesComponentType(ctx, sel)
		Expect(err).ToNot(HaveOccurred())
		Expect(matches).To(BeFalse())
	})
})

var _ = Describe("ExtractGroupKeysFor", func() {
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p",
		Labels: map[string]string{"leaderworkerset.sigs.k8s.io/name": "lws-a"}}}

	It("should evaluate CEL expressions including a coalesced default", func() {
		member := v1alpha1.PodGroupMemberDefinition{
			ComponentName: "group",
			GroupByExpressions: []string{
				`object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/name"].orValue(null)`,
				`([dyn(object[?"metadata"][?"labels"][?"leaderworkerset.sigs.k8s.io/group-index"].orValue(null))].filter(v, v != null && v != false) + ["0"])[0]`,
			},
		}
		keys, err := NewPodQuerier(pod).ExtractGroupKeysFor(ctx, member)
		Expect(err).NotTo(HaveOccurred())
		Expect(keys).To(Equal([]string{"lws-a", "0"}))
	})

})
