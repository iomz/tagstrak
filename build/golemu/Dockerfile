FROM golang:1.25-alpine AS build

LABEL maintainer="Iori Mizutani <iori.mizutani@gmail.com>"

# build the app
RUN mkdir -p /build
WORKDIR /build
COPY go.mod .
COPY go.sum .
COPY cmd/golemu cmd/golemu
COPY internal/binutil internal/binutil
COPY internal/app/golemu internal/app/golemu
COPY internal/emulator internal/emulator
COPY internal/inventory internal/inventory
COPY llrp llrp
RUN go mod vendor
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o golemu ./cmd/golemu/...

# export to a single layer image
FROM alpine:latest

# install some required binaries
RUN apk add --no-cache ca-certificates \
    ffmpeg \
    tzdata

WORKDIR /app

COPY --from=build /build/golemu /app/golemu

# set timezone
ENV TZ "Asia/Tokyo"

ENTRYPOINT ["/app/golemu"]
