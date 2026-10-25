// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package kartadefinitioninventory

import (
	"errors"
	"math/rand"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	v1alpha1 "github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/catalog"
)

var (
	deploymentGVK  = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	jobGVK         = schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}
	podGVK         = schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	dynamoAlphaGVK = schema.GroupVersionKind{Group: "nvidia.com", Version: "v1alpha1", Kind: "DynamoGraphDeployment"}
	dynamoBetaGVK  = schema.GroupVersionKind{Group: "nvidia.com", Version: "v1beta1", Kind: "DynamoGraphDeployment"}

	epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	everything = sourceFilter{enabled: true}
	disabled   = sourceFilter{}
)

func newKarta(name string, gvk schema.GroupVersionKind, opts ...func(*v1alpha1.Karta)) *v1alpha1.Karta {
	k := newRootlessKarta(name)
	k.Spec.StructureDefinition.RootComponent.Kind = &v1alpha1.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}
	for _, opt := range opts {
		opt(k)
	}
	return k
}

func newRootlessKarta(name string) *v1alpha1.Karta {
	return &v1alpha1.Karta{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.KartaSpec{
			StructureDefinition: v1alpha1.StructureDefinition{
				RootComponent: v1alpha1.ComponentDefinition{Name: "root"},
			},
		},
	}
}

func createdAt(t time.Time) func(*v1alpha1.Karta) {
	return func(k *v1alpha1.Karta) { k.CreationTimestamp = metav1.NewTime(t) }
}

func withLabels(l map[string]string) func(*v1alpha1.Karta) {
	return func(k *v1alpha1.Karta) { k.Labels = l }
}

func fromCluster(uid, resourceVersion string) func(*v1alpha1.Karta) {
	return func(k *v1alpha1.Karta) {
		k.UID = types.UID(uid)
		k.ResourceVersion = resourceVersion
	}
}

// invalid empties the root component name, which the SDK validator rejects.
func invalid(k *v1alpha1.Karta) {
	k.Spec.StructureDefinition.RootComponent.Name = ""
}

func mustParseSelector(s string) labels.Selector {
	selector, err := labels.Parse(s)
	if err != nil {
		panic(err)
	}
	return selector
}

func selectedNames(s snapshot) []string {
	GinkgoHelper()
	listed, err := s.list()
	Expect(err).NotTo(HaveOccurred())
	names := make([]string, 0, len(listed))
	for _, sel := range listed {
		names = append(names, sel.Definition.Name)
	}
	return names
}

var _ = Describe("resolve", func() {
	DescribeTable("filters within one source",
		func(filter sourceFilter, wantSelected []string) {
			definitions := []*v1alpha1.Karta{
				newKarta("a", deploymentGVK, withLabels(map[string]string{"team": "x"})),
				newKarta("b", jobGVK),
				newKarta("c", podGVK, withLabels(map[string]string{"team": "y"})),
			}

			snap := resolve(nil, definitions, disabled, filter)

			Expect(selectedNames(snap)).To(Equal(wantSelected))
		},
		Entry("no include list and no selector selects everything",
			everything, []string{"a", "b", "c"}),
		Entry("an include list selects by name",
			sourceFilter{enabled: true, include: sets.New("a")}, []string{"a"}),
		Entry("a selector selects by label",
			sourceFilter{enabled: true, selector: labels.SelectorFromSet(labels.Set{"team": "x"})}, []string{"a"}),
		Entry("include list and selector are a union",
			sourceFilter{enabled: true, include: sets.New("b"), selector: labels.SelectorFromSet(labels.Set{"team": "y"})},
			[]string{"b", "c"}),
		Entry("exclude wins over include",
			sourceFilter{enabled: true, include: sets.New("a", "b"), exclude: sets.New("a")}, []string{"b"}),
		Entry("exclude wins over the selector",
			sourceFilter{enabled: true, exclude: sets.New("c"), selector: mustParseSelector("team")}, []string{"a"}),
		Entry("an exclude list alone removes names from everything",
			sourceFilter{enabled: true, exclude: sets.New("b")}, []string{"a", "c"}),
		Entry("NotIn also matches definitions without the label",
			sourceFilter{enabled: true, selector: mustParseSelector("team notin (x)")}, []string{"b", "c"}),
		Entry("an empty selector counts as absent",
			sourceFilter{enabled: true, include: sets.New("a"), selector: labels.Everything()}, []string{"a"}),
		Entry("a disabled source contributes nothing",
			sourceFilter{include: sets.New("a")}, []string{}),
	)

	Context("precedence between sources", func() {
		It("lets a cluster definition shadow a catalog one on the same group and kind, across versions", func() {
			catalogDefinitions := []*v1alpha1.Karta{
				newKarta("catalog-dynamo-beta", dynamoBetaGVK),
				newKarta("catalog-job", jobGVK),
			}
			clusterDefinitions := []*v1alpha1.Karta{
				newKarta("cluster-dynamo-alpha", dynamoAlphaGVK, fromCluster("uid-1", "7")),
			}

			snap := resolve(catalogDefinitions, clusterDefinitions, everything, everything)

			dynamo, err := snap.lookup(dynamoBetaGVK)
			Expect(err).NotTo(HaveOccurred())
			Expect(dynamo.GVK).To(Equal(dynamoAlphaGVK))
			Expect(dynamo.Origin).To(Equal(Origin{Source: SourceCluster, Name: "cluster-dynamo-alpha", UID: "uid-1", ResourceVersion: "7"}))
			Expect(dynamo.Definition.Name).To(Equal("cluster-dynamo-alpha"))

			job, err := snap.lookup(jobGVK)
			Expect(err).NotTo(HaveOccurred())
			Expect(job.Origin).To(Equal(Origin{Source: SourceCatalog, Name: "catalog-job"}))

		})

		It("unions disjoint sources", func() {
			snap := resolve(
				[]*v1alpha1.Karta{newKarta("catalog-job", jobGVK)},
				[]*v1alpha1.Karta{newKarta("cluster-deployment", deploymentGVK)},
				everything, everything)

			Expect(selectedNames(snap)).To(Equal([]string{"cluster-deployment", "catalog-job"}))
		})

		It("serves the catalog definition again once the cluster one is gone", func() {
			catalogDefinitions := []*v1alpha1.Karta{newKarta("catalog-job", jobGVK)}
			clusterDefinitions := []*v1alpha1.Karta{newKarta("cluster-job", jobGVK)}

			before := resolve(catalogDefinitions, clusterDefinitions, everything, everything)
			after := resolve(catalogDefinitions, nil, everything, everything)

			Expect(selectedNames(before)).To(Equal([]string{"cluster-job"}))
			Expect(selectedNames(after)).To(Equal([]string{"catalog-job"}))
		})

		It("does not let an excluded cluster definition shadow the catalog", func() {
			snap := resolve(
				[]*v1alpha1.Karta{newKarta("catalog-job", jobGVK)},
				[]*v1alpha1.Karta{newKarta("cluster-job", jobGVK)},
				everything, sourceFilter{enabled: true, exclude: sets.New("cluster-job")})

			Expect(selectedNames(snap)).To(Equal([]string{"catalog-job"}))
		})
	})

	DescribeTable("cluster ties",
		func(definitions []*v1alpha1.Karta, wantWinner string) {
			snap := resolve(nil, definitions, disabled, everything)

			sel, err := snap.lookup(jobGVK)
			Expect(err).NotTo(HaveOccurred())
			Expect(sel.Definition.Name).To(Equal(wantWinner))
		},
		Entry("the oldest definition wins regardless of name",
			[]*v1alpha1.Karta{
				newKarta("a-newer", jobGVK, createdAt(epoch.Add(time.Hour))),
				newKarta("b-older", jobGVK, createdAt(epoch)),
			},
			"b-older"),
		Entry("equal creation times fall back to name",
			[]*v1alpha1.Karta{
				newKarta("b", jobGVK, createdAt(epoch)),
				newKarta("a", jobGVK, createdAt(epoch)),
			},
			"a"),
		Entry("a missing creation time counts as oldest",
			[]*v1alpha1.Karta{
				newKarta("a", jobGVK, createdAt(epoch)),
				newKarta("z-unstamped", jobGVK),
			},
			"z-unstamped"),
		Entry("the oldest of three wins",
			[]*v1alpha1.Karta{
				newKarta("c", jobGVK, createdAt(epoch.Add(2*time.Hour))),
				newKarta("a", jobGVK, createdAt(epoch.Add(time.Hour))),
				newKarta("b", jobGVK, createdAt(epoch)),
			},
			"b"),
	)

	DescribeTable("catalog ties pick the highest Kubernetes version priority, then the name",
		func(definitions []*v1alpha1.Karta, wantWinner string) {
			snap := resolve(definitions, nil, everything, disabled)

			sel, err := snap.lookup(dynamoAlphaGVK)
			Expect(err).NotTo(HaveOccurred())
			Expect(sel.Definition.Name).To(Equal(wantWinner))
		},
		Entry("v1beta1 beats v1alpha1 even when its name sorts last",
			[]*v1alpha1.Karta{
				newKarta("aaa-alpha", dynamoAlphaGVK),
				newKarta("zzz-beta", dynamoBetaGVK),
			},
			"zzz-beta"),
		Entry("v1alpha2 beats v1alpha1",
			[]*v1alpha1.Karta{
				newKarta("alpha2", dynamoAlphaGVK.GroupKind().WithVersion("v1alpha2")),
				newKarta("alpha1", dynamoAlphaGVK),
			},
			"alpha2"),
		Entry("a GA version beats a newer alpha",
			[]*v1alpha1.Karta{
				newKarta("v2alpha1", dynamoAlphaGVK.GroupKind().WithVersion("v2alpha1")),
				newKarta("v1", dynamoAlphaGVK.GroupKind().WithVersion("v1")),
			},
			"v1"),
		Entry("v1 beats v1beta1",
			[]*v1alpha1.Karta{
				newKarta("beta", dynamoBetaGVK),
				newKarta("ga", dynamoAlphaGVK.GroupKind().WithVersion("v1")),
			},
			"ga"),
	)

	DescribeTable("selected definitions whose root names no complete GVK are reported, not keyed",
		func(definition *v1alpha1.Karta) {
			snap := resolve(nil, []*v1alpha1.Karta{definition}, disabled, everything)

			Expect(snap.selections).To(BeEmpty())
			Expect(snap.unkeyable).To(Equal([]string{definition.Name}))
		},
		Entry("nil root kind", newRootlessKarta("no-kind")),
		Entry("empty version", newKarta("no-version", schema.GroupVersionKind{Group: "batch", Kind: "Job"})),
		Entry("empty kind", newKarta("no-kind-name", schema.GroupVersionKind{Group: "batch", Version: "v1"})),
	)

	DescribeTable("unkeyable definitions the filters drop are not reported",
		func(filter sourceFilter) {
			snap := resolve(nil, []*v1alpha1.Karta{newRootlessKarta("no-kind"), newKarta("job", jobGVK)}, disabled, filter)

			Expect(snap.unkeyable).To(BeEmpty())
			Expect(selectedNames(snap)).To(Equal([]string{"job"}))
		},
		Entry("excluded", sourceFilter{enabled: true, exclude: sets.New("no-kind")}),
		Entry("not included", sourceFilter{enabled: true, include: sets.New("job")}),
	)

	It("keys a core-group definition, where an empty group is legitimate", func() {
		snap := resolve([]*v1alpha1.Karta{newKarta("core-pod-v1", podGVK)}, nil, everything, disabled)

		sel, err := snap.lookup(podGVK)
		Expect(err).NotTo(HaveOccurred())
		Expect(sel.GVK).To(Equal(podGVK))
		Expect(snap.selections).To(HaveKey(schema.GroupKind{Kind: "Pod"}))
	})

	Context("unmatched cluster include names", func() {
		It("reports the configured names that match no definition, sorted", func() {
			snap := resolve(nil,
				[]*v1alpha1.Karta{newKarta("present", jobGVK)},
				disabled, sourceFilter{enabled: true, include: sets.New("present", "missing-b", "missing-a")})

			Expect(selectedNames(snap)).To(Equal([]string{"present"}))
			Expect(snap.unmatchedIncludes).To(Equal([]string{"missing-a", "missing-b"}))
		})

		It("reports nothing when every name matches or the source is disabled", func() {
			definitions := []*v1alpha1.Karta{newKarta("present", jobGVK)}

			Expect(resolve(nil, definitions, disabled, sourceFilter{enabled: true, include: sets.New("present")}).unmatchedIncludes).To(BeEmpty())
			Expect(resolve(nil, definitions, disabled, sourceFilter{include: sets.New("missing")}).unmatchedIncludes).To(BeEmpty())
		})
	})

	Context("determinism", func() {
		catalogDefinitions := []*v1alpha1.Karta{
			newKarta("catalog-dynamo-alpha", dynamoAlphaGVK),
			newKarta("catalog-dynamo-beta", dynamoBetaGVK),
			newKarta("catalog-job", jobGVK),
			newKarta("core-pod-v1", podGVK),
		}
		clusterDefinitions := []*v1alpha1.Karta{
			newKarta("job-c", jobGVK, createdAt(epoch.Add(2*time.Hour))),
			newKarta("job-a", jobGVK, createdAt(epoch.Add(time.Hour))),
			newKarta("job-b", jobGVK, createdAt(epoch)),
			newKarta("deployment", deploymentGVK),
			newKarta("excluded", deploymentGVK),
			newRootlessKarta("rootless"),
		}
		catalogFilter := sourceFilter{enabled: true, exclude: sets.New("core-pod-v1")}
		clusterFilter := sourceFilter{enabled: true, exclude: sets.New("excluded")}

		It("produces the same snapshot for any input order", func() {
			want := resolve(catalogDefinitions, clusterDefinitions, catalogFilter, clusterFilter)
			wantListed, err := want.list()
			Expect(err).NotTo(HaveOccurred())
			Expect(wantListed).To(HaveLen(3))
			Expect(want.unkeyable).To(Equal([]string{"rootless"}))

			for seed := int64(1); seed <= 20; seed++ {
				shuffledCatalog := slices.Clone(catalogDefinitions)
				shuffledCluster := slices.Clone(clusterDefinitions)
				random := rand.New(rand.NewSource(seed))
				random.Shuffle(len(shuffledCatalog), func(i, j int) {
					shuffledCatalog[i], shuffledCatalog[j] = shuffledCatalog[j], shuffledCatalog[i]
				})
				random.Shuffle(len(shuffledCluster), func(i, j int) {
					shuffledCluster[i], shuffledCluster[j] = shuffledCluster[j], shuffledCluster[i]
				})

				got := resolve(shuffledCatalog, shuffledCluster, catalogFilter, clusterFilter)

				gotListed, err := got.list()
				Expect(err).NotTo(HaveOccurred())
				Expect(gotListed).To(Equal(wantListed), "seed %d", seed)
				Expect(got.unmatchedIncludes).To(Equal(want.unmatchedIncludes), "seed %d", seed)
				Expect(got.unkeyable).To(Equal(want.unkeyable), "seed %d", seed)
			}
		})

		It("does not reorder the caller's slices", func() {
			catalogBefore := slices.Clone(catalogDefinitions)
			clusterBefore := slices.Clone(clusterDefinitions)

			resolve(catalogDefinitions, clusterDefinitions, catalogFilter, clusterFilter)

			Expect(catalogDefinitions).To(Equal(catalogBefore))
			Expect(clusterDefinitions).To(Equal(clusterBefore))
		})
	})

	Context("snapshot reads", func() {
		var snap snapshot

		BeforeEach(func() {
			snap = resolve(
				[]*v1alpha1.Karta{newKarta("catalog-job", jobGVK), newKarta("core-pod-v1", podGVK)},
				[]*v1alpha1.Karta{newKarta("cluster-deployment", deploymentGVK, withLabels(map[string]string{"team": "x"}))},
				everything, everything)
		})

		It("ignores the requested version", func() {
			for _, v := range []string{"v1", "v1beta1", "v9"} {
				sel, err := snap.lookup(jobGVK.GroupKind().WithVersion(v))
				Expect(err).NotTo(HaveOccurred(), v)
				Expect(sel.GVK).To(Equal(jobGVK), v)
				Expect(sel.Origin.Name).To(Equal("catalog-job"), v)
			}
		})

		It("answers an unknown group and kind with a zero selection and no error", func() {
			sel, err := snap.lookup(dynamoAlphaGVK)
			Expect(err).NotTo(HaveOccurred())
			Expect(sel).To(Equal(Selection{}))

			_, ok := snap.elected(dynamoAlphaGVK)
			Expect(ok).To(BeFalse())
		})

		It("returns copies that do not alias the snapshot", func() {
			first, err := snap.lookup(deploymentGVK)
			Expect(err).NotTo(HaveOccurred())
			first.Definition.Name = "mutated"
			first.Definition.Labels["team"] = "mutated"
			first.Definition.Spec.StructureDefinition.RootComponent.Kind.Kind = "Mutated"

			second, err := snap.lookup(deploymentGVK)
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Definition.Name).To(Equal("cluster-deployment"))
			Expect(second.Definition.Labels).To(Equal(map[string]string{"team": "x"}))
			Expect(second.GVK).To(Equal(deploymentGVK))

			listed, err := snap.list()
			Expect(err).NotTo(HaveOccurred())
			listed[0].Definition.Name = "mutated-again"
			elected, ok := snap.elected(deploymentGVK)
			Expect(ok).To(BeTrue())
			elected.Definition.Name = "mutated-too"
			Expect(selectedNames(snap)[0]).To(Equal("cluster-deployment"))
		})

		It("lists selections sorted by group and kind", func() {
			listed, err := snap.list()
			Expect(err).NotTo(HaveOccurred())

			names := make([]string, 0, len(listed))
			kinds := make([]schema.GroupKind, 0, len(listed))
			for _, sel := range listed {
				names = append(names, sel.Origin.Name)
				kinds = append(kinds, sel.GVK.GroupKind())
			}
			Expect(kinds).To(Equal([]schema.GroupKind{deploymentGVK.GroupKind(), jobGVK.GroupKind(), podGVK.GroupKind()}))
			Expect(names).To(Equal([]string{"cluster-deployment", "catalog-job", "core-pod-v1"}))
		})
	})

	Context("validation", func() {
		It("reports an invalid cluster winner instead of falling back to the catalog", func() {
			snap := resolve(
				[]*v1alpha1.Karta{newKarta("catalog-job", jobGVK)},
				[]*v1alpha1.Karta{newKarta("cluster-job", jobGVK, invalid)},
				everything, everything)

			sel, err := snap.lookup(jobGVK)
			Expect(err).To(MatchError(ErrInvalidDefinition))
			Expect(err).To(MatchError(ContainSubstring(`"cluster-job"`)))
			Expect(sel).To(Equal(Selection{}))
		})

		It("reports an invalid winner instead of falling back to a cluster definition it outranked", func() {
			snap := resolve(nil,
				[]*v1alpha1.Karta{
					newKarta("older-invalid", jobGVK, createdAt(epoch), invalid),
					newKarta("newer-valid", jobGVK, createdAt(epoch.Add(time.Hour))),
				},
				disabled, everything)

			_, err := snap.lookup(jobGVK)
			Expect(err).To(MatchError(ErrInvalidDefinition))
			Expect(err).To(MatchError(ContainSubstring(`"older-invalid"`)))
		})

		It("accepts a definition without a root status definition and returns it unchanged", func() {
			definition := newKarta("no-status", jobGVK)
			Expect(definition.Spec.StructureDefinition.RootComponent.StatusDefinition).To(BeNil())

			sel, err := resolve(nil, []*v1alpha1.Karta{definition}, disabled, everything).lookup(jobGVK)

			Expect(err).NotTo(HaveOccurred())
			Expect(sel.Definition.Spec.StructureDefinition.RootComponent.StatusDefinition).To(BeNil())
			Expect(definition.Spec.StructureDefinition.RootComponent.StatusDefinition).To(BeNil())
		})

		It("rejects a core kind other than Pod, as the SDK validator does", func() {
			service := schema.GroupVersionKind{Version: "v1", Kind: "Service"}

			_, err := resolve(nil, []*v1alpha1.Karta{newKarta("service", service)}, disabled, everything).lookup(service)

			Expect(err).To(MatchError(ErrInvalidDefinition))
		})

		It("lists invalid winners with their kinds and joins one error per invalid winner", func() {
			snap := resolve(nil,
				[]*v1alpha1.Karta{
					newKarta("deployment", deploymentGVK),
					newKarta("job", jobGVK, invalid),
				},
				disabled, everything)

			listed, err := snap.list()

			Expect(err).To(MatchError(ErrInvalidDefinition))
			Expect(err.Error()).To(ContainSubstring(`"job"`))
			Expect(err.Error()).NotTo(ContainSubstring(`"deployment"`))
			Expect(listed).To(HaveLen(2))
			Expect(listed[1].GVK).To(Equal(jobGVK))
			Expect(listed[1].Definition.Name).To(Equal("job"))
		})

		It("elects an invalid winner without validating it", func() {
			snap := resolve(nil, []*v1alpha1.Karta{newKarta("job", jobGVK, invalid)}, disabled, everything)

			sel, ok := snap.elected(jobGVK)

			Expect(ok).To(BeTrue())
			Expect(sel.Origin).To(Equal(Origin{Source: SourceCluster, Name: "job"}))
			Expect(sel.Definition.Name).To(Equal("job"))
		})

		It("validates each candidate lazily and once, under concurrent reads", func() {
			var calls atomic.Int32
			c := newCandidate(newKarta("job", jobGVK), SourceCluster, func(*v1alpha1.Karta) error {
				calls.Add(1)
				return errors.New("invalid test definition")
			})
			snap := snapshot{selections: map[schema.GroupKind]candidate{jobGVK.GroupKind(): c}}

			_, ok := snap.elected(jobGVK)
			Expect(ok).To(BeTrue())
			Expect(calls.Load()).To(BeZero())

			var wg sync.WaitGroup
			for range 20 {
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := snap.lookup(jobGVK)
					Expect(err).To(MatchError(ErrInvalidDefinition))
					_, err = snap.list()
					Expect(err).To(MatchError(ErrInvalidDefinition))
				}()
			}
			wg.Wait()
			Expect(calls.Load()).To(Equal(int32(1)))
		})

		It("accepts every catalog winner", func() {
			listed, err := resolve(catalog.List(), nil, everything, disabled).list()

			Expect(err).NotTo(HaveOccurred())
			Expect(listed).NotTo(BeEmpty())
		})
	})
})
