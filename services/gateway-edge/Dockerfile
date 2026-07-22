# syntax=docker/dockerfile:1
# Multi-stage build: rust:1.80-bookworm -> debian:bookworm-slim (SPEC §15).

FROM rust:1.80-bookworm AS build
WORKDIR /app

# cmake is required by rdkafka's `cmake-build` feature (bundled librdkafka).
RUN apt-get update \
    && apt-get install -y --no-install-recommends cmake build-essential pkg-config libssl-dev \
    && rm -rf /var/lib/apt/lists/*

# Dependency caching layer.
COPY Cargo.toml ./
RUN mkdir -p src && echo 'fn main() {}' > src/main.rs \
    && cargo build --release \
    && rm -rf src

COPY src ./src
RUN touch src/main.rs && cargo build --release

# To include the live Fluvio consumer (default build ships a stub):
#   docker build --build-arg CARGO_FEATURES="--features fluvio-live" ...
ARG CARGO_FEATURES=""
RUN if [ -n "$CARGO_FEATURES" ]; then cargo build --release $CARGO_FEATURES; fi

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 10001 opendesk
USER opendesk
COPY --from=build /app/target/release/gateway-edge /usr/local/bin/gateway-edge
EXPOSE 7005
ENV RUST_LOG=info
ENTRYPOINT ["gateway-edge"]
