// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
)

type blockedOwnerMetadata struct {
	metadata.Interface
	started chan string
	release chan struct{}
}

type blockedOwnerResource struct {
	metadata.Getter
	client *blockedOwnerMetadata
}

func (c *blockedOwnerMetadata) Resource(resource schema.GroupVersionResource) metadata.Getter {
	return &blockedOwnerResource{Getter: c.Interface.Resource(resource), client: c}
}

func (r *blockedOwnerResource) Namespace(string) metadata.ResourceInterface { return r }

func (r *blockedOwnerResource) Get(ctx context.Context, name string, _ metav1.GetOptions, _ ...string) (*metav1.PartialObjectMetadata, error) {
	r.client.started <- name
	select {
	case <-r.client.release:
		return nil, fmt.Errorf("owner read released")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var _ = Describe("Root publication capacity", func() {
	It("publishes a healthy root update while candidate owner reads occupy every ownership worker", func(ctx SpecContext) {
		r, client, publications := testRuntime(nil)
		blocked := &blockedOwnerMetadata{Interface: client, started: make(chan string, r.config.RootWorkers), release: make(chan struct{})}
		r.reader.clients.Metadata = blocked
		root := testObject(testRootGVK, "healthy")
		watch := seedWatch(r, testRootGVK, "jobs", root)

		workerCtx, cancel := context.WithCancel(ctx)
		var workers sync.WaitGroup
		DeferCleanup(func() {
			cancel()
			close(blocked.release)
			r.rootQueue.ShutDown()
			r.inputQueue.ShutDown()
			workers.Wait()
		})
		// Use the production queue consumers with the configured worker counts.
		// Seeded watches avoid unrelated reflector and source maintenance work.
		for i := 0; i < r.config.RootWorkers; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				runWorker(workerCtx, r.rootQueue, func(ctx context.Context, key rootKey) error { return r.reconcile(ctx, key.UID) })
			}()
		}
		for i := 0; i < r.config.LeafWorkers; i++ {
			workers.Add(1)
			go func() { defer workers.Done(); runWorker(workerCtx, r.inputQueue, r.resolveInput) }()
		}
		Eventually(func() string {
			if record := publications.Published(root.GetUID()); record != nil {
				return record.Workload.Ref.ResourceVersion
			}
			return ""
		}, 2*time.Second).Should(Equal("1"))
		valid, _ := publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeTrue())

		for i := 0; i < r.config.RootWorkers; i++ {
			candidate := testObject(testRootGVK, fmt.Sprintf("candidate-%d", i))
			owner := testObject(testRootGVK, fmt.Sprintf("blocked-owner-%d", i))
			ownedBy(candidate, owner)
			r.acceptEvent(watch, candidate, false)
		}
		started := map[string]bool{}
		for i := 0; i < r.config.RootWorkers; i++ {
			var name string
			Eventually(blocked.started, 2*time.Second).Should(Receive(&name))
			started[name] = true
		}
		Expect(started).To(HaveLen(r.config.RootWorkers), "each blocked GET must belong to a different candidate")

		updated := root.DeepCopy()
		updated.SetResourceVersion("2")
		r.acceptEvent(watch, updated, false)
		accepted, found := r.reader.Cached(refOf(updated))
		Expect(found).To(BeTrue())
		Expect(accepted.GetResourceVersion()).To(Equal("2"))
		Eventually(func() string {
			if record := publications.Published(root.GetUID()); record != nil {
				return record.Workload.Ref.ResourceVersion
			}
			return ""
		}, 2*time.Second).Should(Equal("2"), "healthy publication must advance before unrelated owner reads are released")
		valid, _ = publications.Capture().Valid(publications.Published(root.GetUID()).Creation)
		Expect(valid).To(BeTrue())
	})
})
