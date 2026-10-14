# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
FROM --platform=$BUILDPLATFORM golang:1.26.3 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
WORKDIR /src/exporter
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /workload-gpu-observer ./examples/workload-gpu-observer
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /workload-gpu-observer /workload-gpu-observer
USER 65532:65532
ENTRYPOINT ["/workload-gpu-observer"]
