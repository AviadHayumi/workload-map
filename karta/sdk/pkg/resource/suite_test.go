// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/cel"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/expression"
)

func TestKarta(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Karta Suite")
}

// mustCelRunner builds the engine over an object for tests; construction only fails on an
// unencodable object, which a fixture never is.
func mustCelRunner(object any) expression.Runner {
	runner, err := cel.NewRunner(object)
	if err != nil {
		panic(err)
	}

	return runner
}
