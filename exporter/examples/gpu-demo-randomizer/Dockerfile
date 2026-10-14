# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
ARG GO_VERSION=1.26.3
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
WORKDIR /src/exporter
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /gpu-demo-randomizer ./examples/gpu-demo-randomizer
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /gpu-demo-randomizer /gpu-demo-randomizer
USER 65532:65532
ENTRYPOINT ["/gpu-demo-randomizer"]
