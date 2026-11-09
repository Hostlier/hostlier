# Build a static binary on the builder architecture for the requested Linux target.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
ARG TARGETARCH
WORKDIR /src
COPY . .
RUN ./build.sh "$TARGETARCH"

# No shell, package manager, application listeners, or unused Collector components.
FROM scratch
ARG TARGETARCH
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /src/dist/hostlier-collector-linux-${TARGETARCH} /hostlier-collector
USER 0:0
ENTRYPOINT ["/hostlier-collector"]
CMD ["--config=/etc/otelcol/collector.yaml"]
