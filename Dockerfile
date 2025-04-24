FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS builder-go

ARG TARGETOS TARGETARCH
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN apt-get update && apt-get install -y libzmq3-dev wget && apt-get clean && rm -rf /var/lib/apt/lists/*

COPY spark spark

RUN cd spark && go install -v bin/operator/main.go
RUN if [ -e /go/bin/${TARGETOS}_${TARGETARCH} ]; then mv /go/bin/${TARGETOS}_${TARGETARCH}/* /go/bin/; fi

# Healthcheck
RUN GRPC_HEALTH_PROBE_VERSION=v0.4.13 && \
    wget -qO/bin/grpc_health_probe https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-${TARGETOS}-${TARGETARCH} && \
    chmod +x /bin/grpc_health_probe

FROM --platform=$BUILDPLATFORM rust:1.84-slim-bookworm AS builder-rust

ARG TARGETOS TARGETARCH
RUN echo "$TARGETARCH" | sed 's,arm,aarch,;s,amd,x86_,' > /tmp/arch

RUN apt-get update && apt-get install -y protobuf-compiler "gcc-$(tr _ - < /tmp/arch)-linux-gnu" "g++-$(tr _ - < /tmp/arch)-linux-gnu" && apt-get clean && rm -rf /var/lib/apt/lists/*
RUN rustup target add "$(cat /tmp/arch)-unknown-${TARGETOS}-gnu"

COPY protos protos
COPY signer signer

RUN cd signer && cargo build --target "$(cat /tmp/arch)-unknown-${TARGETOS}-gnu" --release

FROM --platform=$BUILDPLATFORM arigaio/atlas:0.31.0 AS atlas

FROM debian:bookworm-slim AS final

RUN addgroup --system --gid 1000 spark
RUN adduser --system --uid 1000 --home /home/spark --ingroup spark spark

RUN apt-get update && apt-get -y install libzmq5 ca-certificates gettext-base && rm -rf /var/lib/apt/lists

EXPOSE 9735 10009
ENTRYPOINT ["spark-operator"]

COPY --from=atlas /atlas /usr/local/bin/atlas
COPY --from=builder-go /go/bin/main /usr/local/bin/spark-operator
COPY --from=builder-go /bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=builder-rust /signer/target/*/release/spark-frost-signer /usr/local/bin/spark-frost-signer
COPY spark/so/ent/migrate/migrations /opt/spark/migrations

# Install security updates
RUN apt-get update && apt-get -y upgrade && apt-get clean && rm -rf /var/lib/apt/lists
