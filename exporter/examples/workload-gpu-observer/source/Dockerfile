# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation
FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
RUN git init && git remote add origin https://github.com/run-ai/fake-gpu-operator.git && git fetch --depth=1 origin 0901c8fc9c235e5b2ef8a2876fb89db1f7af23f4 && git checkout --detach FETCH_HEAD && test "$(git rev-parse HEAD)" = 0901c8fc9c235e5b2ef8a2876fb89db1f7af23f4
COPY exporter/examples/workload-gpu-observer/source/main.go.txt cmd/karta-demo-source/main.go
COPY exporter/examples/workload-gpu-observer/source/main_test.go.txt cmd/karta-demo-source/main_test.go
RUN go test ./cmd/karta-demo-source
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /fake-gpu-source ./cmd/karta-demo-source
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /fake-gpu-source /fake-gpu-source
COPY --from=build /src/LICENSE /licenses/fake-gpu-operator-LICENSE
USER 65532:65532
ENTRYPOINT ["/fake-gpu-source"]
