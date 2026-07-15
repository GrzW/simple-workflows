FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w" \
      -o /build/engine \
      ./cmd/engine

RUN mkdir -p /data && chown -R 65532:65532 /data

FROM gcr.io/distroless/static-debian12 AS final

# Run as the built-in non-root user (UID 65532 = "nonroot" in distroless)
USER 65532:65532

WORKDIR /app

COPY --from=builder --chown=65532:65532 /build/engine /app/engine
COPY --from=builder --chown=65532:65532 /data /data

EXPOSE 8080

ENTRYPOINT ["/app/engine"]
