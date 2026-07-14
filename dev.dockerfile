# Base stage — install deps, modules, circuits (shared by all targets)
FROM golang:1.26 AS base

WORKDIR /src

ENV CGO_ENABLED=1 \
    GOCACHE=/go-cache \
    GOPATH=/go

RUN apt-get update && \
    apt-get install --no-install-recommends -y \
      libc6-dev libomp-dev openmpi-common libgomp1 ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go-cache go mod download

COPY . .
RUN go run scripts/circuits/main.go

ENV WASMER_PATH=/go/pkg/mod/github.com/wasmerio/wasmer-go@v1.0.4/wasmer/packaged/lib/linux-amd64
ENV LD_LIBRARY_PATH=${WASMER_PATH}

# API target
FROM base AS api
RUN go build -o /usr/local/bin/api ./cmd/service/main.go
CMD ["/usr/local/bin/api"]

# Local SMTP target (used for local testing)
FROM base AS localsmtp
RUN go build -o /usr/local/bin/localsmtp ./cmd/localsmtp/main.go
CMD ["/usr/local/bin/localsmtp"]

# Fund account target (used for local testing)
FROM base AS fundaccount
RUN go build -o /usr/local/bin/fundaccount ./scripts/fundaccount/main.go
CMD ["/usr/local/bin/fundaccount"]

# Setup default plan target (used for local testing)
FROM base AS defaultplan
RUN go build -o /usr/local/bin/defaultplan ./scripts/defaultplan/main.go
CMD ["/usr/local/bin/defaultplan"]
