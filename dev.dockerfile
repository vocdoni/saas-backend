# Dev image for local development with `go run`
FROM golang:1.24

WORKDIR /src

# Enable CGO and set caches
ENV CGO_ENABLED=1 \
    GOCACHE=/go-cache \
    GOPATH=/go

# Native dependencies required by rapidsnark/wasmer, TLS, etc.
RUN apt-get update && \
    apt-get install --no-install-recommends -y \
      libc6-dev libomp-dev openmpi-common libgomp1 ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*

# Pre-warm module cache to speed up first run
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go-cache go mod download

# Optional: copy the code so the container can run without compose-watch.
# When using `docker compose watch`, your code will be synced over this path.
COPY . .

# Ensure the dynamic linker can find Wasmer's shared library used by wasmer-go
ENV WASMER_PATH=/go/pkg/mod/github.com/wasmerio/wasmer-go@v1.0.4/wasmer/packaged/lib/linux-amd64
ENV LD_LIBRARY_PATH=${WASMER_PATH}

# Default command for dev: run the service directly
CMD ["go", "run", "./cmd/service/main.go"]
