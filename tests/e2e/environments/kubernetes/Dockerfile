# Build with the same userland image for every permission profile.
# RUNTIME_IMAGE must provide sh, bash and the helpers being tested.
ARG RUNTIME_IMAGE=hostel:dev
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags=e2e -o /out/hostel-e2e ./cmd/hostel && \
    CGO_ENABLED=0 go test -tags=e2e -c -o /out/e2e.test ./tests/e2e

FROM ${RUNTIME_IMAGE}
ARG SOURCE_REVISION=unknown
COPY --from=build /out/ /e2e/
COPY tests/e2e/environments/kubernetes/config.yaml /e2e/config.yaml
ENV HOSTEL_E2E_BINARY=/e2e/hostel-e2e \
    HOSTEL_E2E_ENVIRONMENT_FILE=/e2e/config.yaml \
    HOSTEL_E2E_RUNS_DIR=/e2e/runs \
    HOSTEL_E2E_REVISION=${SOURCE_REVISION} \
    HOSTEL_E2E_ON_TARGET=1
WORKDIR /e2e
ENTRYPOINT ["/e2e/e2e.test"]
CMD ["-test.v", "-test.timeout=30m"]
