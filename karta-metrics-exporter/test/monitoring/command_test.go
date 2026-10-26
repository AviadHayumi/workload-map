//go:build monitoring

// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package monitoring_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Monitoring command output", func() {
	It("keeps warnings out of successful machine-readable output", func(ctx SpecContext) {
		body, err := command(ctx, "sh", "", "-c", "printf '192.0.2.10'; printf 'Warning: deprecated API\\n' >&2")
		Expect(err).NotTo(HaveOccurred())
		Expect(body).To(Equal("192.0.2.10"))
	}, SpecTimeout(5*time.Second))

	It("retains stdout and stderr when a command fails", func(ctx SpecContext) {
		body, err := command(ctx, "sh", "", "-c", "printf 'partial output\\n'; printf 'request failed\\n' >&2; exit 1")
		Expect(err).To(HaveOccurred())
		Expect(body).To(ContainSubstring("partial output\n"))
		Expect(body).To(ContainSubstring("request failed\n"))
	}, SpecTimeout(5*time.Second))
})
