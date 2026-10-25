FROM golang:1.26.10-alpine AS build

LABEL maintainer="Iori Mizutani <iori.mizutani@gmail.com>"

# build the app
RUN mkdir -p /build
WORKDIR /build
COPY go.mod .
COPY go.sum .
COPY cmd/tagstrak-rig cmd/tagstrak-rig
COPY internal/app/rig internal/app/rig
COPY internal/emulator internal/emulator
COPY internal/inventory internal/inventory
COPY internal/reader internal/reader
COPY llrp llrp
RUN go mod vendor
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o tagstrak-rig ./cmd/tagstrak-rig/...

# export to a single layer image
FROM alpine:3.24

# install some required binaries; the ca-certificates floor excludes bundles
# that still trust the compromised eMudhra intermediates (CVE-2026-111943)
RUN apk upgrade --no-cache && \
    apk add --no-cache 'ca-certificates>=20261020-r0' \
    'ca-certificates-bundle>=20261020-r0' \
    ffmpeg \
    tzdata

WORKDIR /app

COPY --from=build /build/tagstrak-rig /app/tagstrak-rig

# set timezone
ENV TZ "Asia/Tokyo"

ENTRYPOINT ["/app/tagstrak-rig"]
