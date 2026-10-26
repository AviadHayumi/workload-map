# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

# Build from the repository root so the SDK replace directive resolves locally.
# Keep GO_VERSION aligned with the `go` directive in exporter/go.mod.
ARG GO_VERSION=1.27.3
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-main
ENV GOWORK=off
ENV GOTOOLCHAIN=local

WORKDIR /src
COPY . .
WORKDIR /src/exporter
RUN CGO_ENABLED=0 GO111MODULE=on GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath \
	-ldflags "-X github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/version.version=${VERSION}" \
	-o /karta-exporter ./cmd

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /karta-exporter /karta-exporter
USER 65532:65532
ENTRYPOINT ["/karta-exporter"]
