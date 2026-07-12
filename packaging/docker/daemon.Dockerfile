FROM golang:1.25-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps apps
COPY go go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/carina-daemon ./apps/carina-daemon
FROM rust:1.96.1-bookworm AS rust
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY crates crates
RUN cargo build --locked --release -p carina-kernel --bin carina-kernel-service
FROM python:3.12-bookworm AS headroom
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
COPY integrations/headroom.lock integrations/headroom-requirements.lock integrations/
COPY scripts/build-headroom-bundle.sh scripts/verify-headroom-bundle.py scripts/
RUN OUTPUT=/out/headroom ./scripts/build-headroom-bundle.sh
FROM debian:bookworm-slim
RUN useradd --system --uid 65532 --create-home carina
COPY --from=go /out/carina-daemon /usr/local/bin/carina-daemon
COPY --from=rust /src/target/release/carina-kernel-service /usr/local/bin/carina-kernel-service
COPY --from=headroom /out/headroom /usr/local/bin/headroom
ENV CARINA_KERNEL_BIN=/usr/local/bin/carina-kernel-service
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/carina-daemon"]
