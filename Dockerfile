FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS builder-go

ARG TARGETOS TARGETARCH
ARG DEBUG=0
ENV GOOS=$TARGETOS
ENV GOARCH=$TARGETARCH

RUN apt-get update && apt-get install -y libzmq3-dev wget && apt-get clean && rm -rf /var/lib/apt/lists/*

RUN go install github.com/go-delve/delve/cmd/dlv@latest

# Copy only go.mod and go.sum first to leverage Docker layer caching
COPY spark/go.mod spark/go.sum spark/

RUN --mount=type=cache,target=/go/pkg/mod \
    cd spark && go mod download

COPY spark spark

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ "$DEBUG" = "1" ]; then \
      cd spark && go build -gcflags="all=-N -l" -o /go/bin/spark-operator bin/operator/main.go ; \
    else \
      cd spark && go build -o /go/bin/spark-operator bin/operator/main.go ; \
    fi

RUN if [ -e /go/bin/${TARGETOS}_${TARGETARCH} ]; then mv /go/bin/${TARGETOS}_${TARGETARCH}/* /go/bin/; fi

# Healthcheck
RUN GRPC_HEALTH_PROBE_VERSION=v0.4.13 && \
    wget -qO/bin/grpc_health_probe https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-${TARGETOS}-${TARGETARCH} && \
    chmod +x /bin/grpc_health_probe


# (1) create rust env with cargo chef crate
FROM --platform=$BUILDPLATFORM rust:1.84-slim-bookworm AS chef
WORKDIR /signer
RUN cargo install cargo-chef

# (2) generate recipe file to prepare dependencies build
FROM chef AS planner-rust
WORKDIR /signer
COPY signer/. ./
RUN cargo chef prepare --recipe-path recipe.json

# (3) build dependencies
FROM chef AS cacher-rust
COPY --from=planner-rust /signer/recipe.json recipe.json
WORKDIR /signer
RUN cargo chef cook --release --recipe-path recipe.json

# (4) build app
FROM chef AS builder-rust
COPY protos protos
WORKDIR /signer
COPY signer/. ./
COPY --from=cacher-rust /signer/target target
COPY --from=cacher-rust /usr/local/cargo /usr/local/cargo
ARG TARGETOS TARGETARCH
RUN echo "$TARGETARCH" | sed 's,arm,aarch,;s,amd,x86_,' > /tmp/arch
RUN apt-get update && apt-get install -y protobuf-compiler "gcc-$(tr _ - < /tmp/arch)-linux-gnu" "g++-$(tr _ - < /tmp/arch)-linux-gnu" && apt-get clean && rm -rf /var/lib/apt/lists/*
RUN rustup target add "$(cat /tmp/arch)-unknown-${TARGETOS}-gnu"
RUN cargo build --target "$(cat /tmp/arch)-unknown-${TARGETOS}-gnu" --release


FROM --platform=$BUILDPLATFORM arigaio/atlas:0.31.0 AS atlas

FROM debian:bookworm-slim AS final

RUN addgroup --system --gid 1000 spark
RUN adduser --system --uid 1000 --home /home/spark --ingroup spark spark

RUN apt-get update && apt-get -y install libzmq5 ca-certificates gettext-base && rm -rf /var/lib/apt/lists

EXPOSE 9735 10009
ENTRYPOINT ["spark-operator"]

ARG DEBUG=0
COPY --from=atlas /atlas /usr/local/bin/atlas
COPY --from=builder-go /go/bin/spark-operator /usr/local/bin/spark-operator
COPY --from=builder-go /go/bin/dlv /usr/local/bin/dlv
COPY --from=builder-go /bin/grpc_health_probe /usr/local/bin/grpc_health_probe
COPY --from=builder-rust /signer/target/*/release/spark-frost-signer /usr/local/bin/spark-frost-signer
COPY spark/so/ent/migrate/migrations /opt/spark/migrations

RUN if [ "$DEBUG" = "0" ]; then rm -f /usr/local/bin/dlv; fi

# Install security updates
RUN apt-get update && apt-get -y upgrade && apt-get clean && rm -rf /var/lib/apt/lists
