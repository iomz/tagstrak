# Builds the tagstrak-rig test rig. From the repository root:
#
#   docker build -f build/tagstrak-rig/Dockerfile -t tagstrak-rig .
#   docker run --rm -p 5084:5084 tagstrak-rig simulator \
#     --scenario /examples/scenario.json --listen 0.0.0.0:5084
FROM golang:1.26.10-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/tagstrak-rig ./cmd/tagstrak-rig

FROM alpine:3.24

LABEL maintainer="Iori Mizutani <iori.mizutani@gmail.com>"

# The ca-certificates floor excludes bundles that still trust the compromised
# eMudhra intermediates (CVE-2026-111943).
RUN apk upgrade --no-cache && \
    apk add --no-cache 'ca-certificates>=20261020-r0' \
    'ca-certificates-bundle>=20261020-r0'

COPY --from=build /out/tagstrak-rig /usr/local/bin/tagstrak-rig
COPY examples/rig /examples

USER 65534:65534
EXPOSE 5084
ENTRYPOINT ["tagstrak-rig"]
