// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestPVCPostHasOneTransportAttempt(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var requests, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method == http.MethodPost {
					posts.Add(1)
				}
				if request.URL.Path != "/api/v1/namespaces/test/persistentvolumeclaims" {
					t.Errorf("unexpected replay/redirect path: %s", request.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable {
					w.Header().Set("Retry-After", "1")
				} else {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(code)
				_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","message":"ambiguous create response","code":%d}`, code)
			}))
			defer server.Close()
			parent := testWorkload()
			parent.SetAnnotations(map[string]string{annotationStorage: "1Gi"})
			r, fake := newTestReconciler(t, parent)
			transport, err := newPVCClient(fake, &rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			r.client = transport
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}); err == nil {
				t.Fatal("ambiguous HTTP response must fail the create")
			}
			if posts.Load() != 1 || requests.Load() != 1 {
				t.Fatalf("transport made %d POSTs and %d total requests; want exactly one", posts.Load(), requests.Load())
			}
			if saved := persistedState(t, fake); saved.Storage == nil || saved.Storage.UID != "" {
				t.Fatalf("reservation lost: %+v", saved)
			}
			// A later reconcile has no in-memory creation permit and cannot replay POST.
			r, _ = newTestReconciler(t)
			r.client, r.reader = transport, fake
			if err := runReconcile(t, r); err != nil {
				t.Fatal(err)
			}
			assertResult(t, fake, "Error", "StorageCreationUncertain")
			if posts.Load() != 1 || requests.Load() != 1 {
				t.Fatal("later reconcile retried the POST")
			}
		})
	}
}

func TestBinaryHelpHasNoFlagCollision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "cel-plugins")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controller: %v\n%s", err, output)
	}
	help := exec.CommandContext(ctx, binary, "--help")
	output, err := help.CombinedOutput()
	if err != nil {
		t.Fatalf("controller --help: %v\n%s", err, output)
	}
	for _, option := range []string{"-namespace", "-kubeconfig", "-storage-class"} {
		if !strings.Contains(string(output), option) {
			t.Fatalf("help is missing %s: %s", option, output)
		}
	}
	invalid := exec.CommandContext(ctx, binary, "--namespace", "")
	output, err = invalid.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "--namespace must be a nonempty Kubernetes namespace") {
		t.Fatalf("invalid startup did not validate arguments: %v\n%s", err, output)
	}
}
