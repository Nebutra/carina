FROM golang:1.25-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY apps apps
COPY go go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/carina-worker ./apps/carina-worker
FROM debian:bookworm-slim
RUN useradd --system --uid 65532 --create-home carina
COPY --from=go /out/carina-worker /usr/local/bin/carina-worker
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/carina-worker"]
