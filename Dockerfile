# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=build_ca \
    if [ -f /run/secrets/build_ca ]; then export SSL_CERT_FILE=/run/secrets/build_ca; fi; \
    go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ecrans .

FROM alpine:3.23
LABEL org.opencontainers.image.title="Saint-André Écrans" \
      org.opencontainers.image.version="1.0.0"
RUN addgroup -S ecrans && adduser -S -G ecrans ecrans \
    && mkdir /data && chown ecrans:ecrans /data
COPY --from=build /ecrans /usr/local/bin/ecrans
USER ecrans
ENV DATA_DIR=/data LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=4s CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/ecrans"]
