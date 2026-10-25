// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/app"
	"github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/internal/scaffold/mocks"
	collector "github.com/dsx-ai-factory/workload-map/karta-metrics-exporter/pkg/adapters/otel"
)

type registration struct {
	metric.Registration
	calls int
	err   error
}

func (r *registration) Unregister() error {
	r.calls++
	return r.err
}

func TestRunLifecycle(t *testing.T) {
	runtimeFailure := errors.New("runtime failed")
	cleanupFailure := errors.New("unregister failed")
	for _, tc := range []struct {
		name              string
		registrationError error
		runtimeError      error
		cleanupError      error
		nilRegistration   bool
	}{
		{name: "success"},
		{name: "registration failure", registrationError: errors.New("registration failed")},
		{name: "runtime failure", runtimeError: runtimeFailure},
		{name: "cleanup failure", cleanupError: cleanupFailure},
		{name: "both failures", runtimeError: runtimeFailure, cleanupError: cleanupFailure},
		{name: "missing registration", nilRegistration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publications := mocks.NewStore(t)
			runtime := mocks.NewRuntime(t)
			lock := &sync.Mutex{}
			bound := false
			publications.EXPECT().BindPublication(lock, mock.Anything).Run(func(*sync.Mutex, func() time.Time) { bound = true }).Once()
			reg := &registration{err: tc.cleanupError}
			registered := false
			ctx := context.Background()
			if tc.registrationError == nil && !tc.nilRegistration {
				runtime.EXPECT().Run(ctx).Run(func(context.Context) {
					require.True(t, registered)
				}).Return(tc.runtimeError).Once()
			}
			components := app.Components{
				Runtime: runtime, Store: publications, PublicationLock: lock,
				Register: func(_ metric.Meter, source collector.Capturer, options collector.Options) (metric.Registration, error) {
					require.True(t, bound)
					require.Same(t, publications, source)
					require.Equal(t, []string{"Running"}, options.WorkloadPhases)
					registered = true
					if tc.registrationError != nil || tc.nilRegistration {
						return nil, tc.registrationError
					}
					return reg, nil
				},
			}
			err := app.Run(ctx, components, noop.NewMeterProvider().Meter("test"), collector.Options{WorkloadPhases: []string{"Running"}})
			switch {
			case tc.registrationError != nil:
				require.ErrorIs(t, err, tc.registrationError)
				require.Zero(t, reg.calls)
			case tc.nilRegistration:
				require.ErrorContains(t, err, "no registration")
				require.Zero(t, reg.calls)
			default:
				require.Equal(t, 1, reg.calls)
				if tc.runtimeError != nil {
					require.ErrorIs(t, err, tc.runtimeError)
				}
				if tc.cleanupError != nil {
					require.ErrorIs(t, err, tc.cleanupError)
				}
				if tc.runtimeError == nil && tc.cleanupError == nil {
					require.NoError(t, err)
				}
			}
		})
	}
}

func TestRunRejectsIncompleteComponents(t *testing.T) {
	require.ErrorContains(t, app.Run(context.Background(), app.Components{}, noop.NewMeterProvider().Meter("test"), collector.Options{}), "incomplete")
}
