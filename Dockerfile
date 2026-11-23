# syntax=docker/dockerfile:1
ARG APP_VERSION=dev
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH APP_VERSION
WORKDIR /src
COPY . .
RUN --mount=type=secret,id=build_ca \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ -f /run/secrets/build_ca ]; then export SSL_CERT_FILE=/run/secrets/build_ca; fi; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w -X main.appVersion=$APP_VERSION" -o /ecrans .

FROM alpine:3.23
ARG APP_VERSION
LABEL org.opencontainers.image.title="Saint André Écrans" \
      org.opencontainers.image.version="$APP_VERSION"
RUN addgroup -S ecrans && adduser -S -G ecrans ecrans \
    && mkdir /data && chown ecrans:ecrans /data
COPY --from=build /ecrans /usr/local/bin/ecrans
USER ecrans
ENV DATA_DIR=/data LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=4s CMD wget -Y off -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/ecrans"]
