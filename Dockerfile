FROM cgr.dev/chainguard/go:latest-dev AS builder

LABEL org.opencontainers.image.authors="r@nice.pink"
LABEL org.opencontainers.image.source="https://github.com/nice-pink/itakeit"

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN ./build && mkdir -p /out/data

FROM cgr.dev/chainguard/glibc-dynamic:latest AS runner

LABEL org.opencontainers.image.authors="r@nice.pink"
LABEL org.opencontainers.image.source="https://github.com/nice-pink/itakeit"

WORKDIR /app
COPY --from=builder /app/bin/itakeit /app/itakeit
# The image runs as uid 65532. A named volume mounted at /data inherits this owner.
COPY --from=builder --chown=65532:65532 /out/data /data
ENTRYPOINT [ "/app/itakeit", "-config", "/config/config.yaml" ]
