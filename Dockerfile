# The compiler always runs natively on the build host ($BUILDPLATFORM) and cross-compiles for
# $TARGETARCH, so multi-arch images do not pay for emulating a Go build under QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /server .

FROM alpine:3.22
RUN apk --no-cache add ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -u 10001 -G app app
WORKDIR /app
COPY --from=builder /server /app/server
USER app
EXPOSE 8080
CMD ["/app/server"]
