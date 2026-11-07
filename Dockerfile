# syntax=docker/dockerfile:1

# ---- build stage -------------------------------------------------
FROM golang:1.26.10-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION
# Static, stripped binary, matching scripts/build.sh.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X github.com/ArpitK24/forge/internal/core.AppVersion=${VERSION:-docker}" -o /out/forge ./cmd/forge

# ---- runtime stage -----------------------------------------------
# Alpine rather than distroless: Forge's bash tool needs a shell,
# and agents routinely run git.
FROM alpine:3.22
RUN apk add --no-cache bash git ca-certificates \
 && adduser -D -h /home/forge forge
COPY --from=build /out/forge /usr/local/bin/forge

USER forge
# Mount the project you want Forge to work on here:
#   docker run -it --rm -v "$PWD":/workspace -e FORGE_API_KEY forge
WORKDIR /workspace
ENTRYPOINT ["forge"]
