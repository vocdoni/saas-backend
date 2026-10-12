ARG GOLANG_VERSION
FROM golang:${GOLANG_VERSION} AS builder

WORKDIR /src
ENV CGO_ENABLED=1
RUN go env -w GOCACHE=/go-cache
COPY . .
RUN --mount=type=cache,target=/go-cache go mod download
RUN --mount=type=cache,target=/go-cache go build -o=backend -ldflags="-s -w" cmd/service/main.go

FROM debian:bookworm-slim AS base

WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

RUN apt-get update && \
	apt-get install --no-install-recommends -y curl && \
	apt-get autoremove -y && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /src/backend ./

ENTRYPOINT ["/app/backend"]
