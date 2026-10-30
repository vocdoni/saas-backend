ARG GOLANG_VERSION
FROM golang:${GOLANG_VERSION} AS builder

WORKDIR /src
ENV CGO_ENABLED=1
RUN go env -w GOCACHE=/go-cache
COPY . .
RUN --mount=type=cache,target=/go-cache go mod download
RUN --mount=type=cache,target=/go-cache go run scripts/circuits/main.go
RUN --mount=type=cache,target=/go-cache go build -o=backend -ldflags="-s -w" cmd/service/main.go

FROM debian:bookworm-slim AS base

WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Support for go-rapidsnark witness calculator (https://github.com/iden3/go-rapidsnark/tree/main/witness)
COPY --from=builder /go/pkg/mod/github.com/wasmerio/wasmer-go@v1.0.4/wasmer/packaged/lib/linux-amd64/libwasmer.so \
                    /go/pkg/mod/github.com/wasmerio/wasmer-go@v1.0.4/wasmer/packaged/lib/linux-amd64/libwasmer.so

# Support for go-rapidsnark prover (https://github.com/iden3/go-rapidsnark/tree/main/prover)
RUN apt-get update && \
	apt-get install --no-install-recommends -y libc6-dev libomp-dev openmpi-common libgomp1 curl && \
	apt-get autoremove -y && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Run as a dedicated unprivileged user with a fixed UID/GID so mounted volumes and
# security policies can rely on stable ids. The service listens on 8080 by default,
# so no extra capabilities are needed to bind it.
ARG APP_UID=10001
ARG APP_GID=10001
RUN groupadd --system --gid ${APP_GID} vocdoni && \
    useradd --system --uid ${APP_UID} --gid ${APP_GID} --home-dir /home/vocdoni \
        --create-home --shell /usr/sbin/nologin vocdoni
# The zk circuit cache path derives from $HOME (~/.cache/vocdoni/zkCircuits); pin it so
# the prefetched artifacts are found even if the runtime overrides the user.
ENV HOME=/home/vocdoni

WORKDIR /app
# The binary stays root-owned (read-only for the service user). Only the circuit cache is
# owned by the service user, since it is downloaded there at runtime if a version is missing.
COPY --from=builder /src/backend ./
COPY --from=builder --chown=${APP_UID}:${APP_GID} /root/.cache/vocdoni/zkCircuits /home/vocdoni/.cache/vocdoni/zkCircuits

USER ${APP_UID}:${APP_GID}

ENTRYPOINT ["/app/backend"]
