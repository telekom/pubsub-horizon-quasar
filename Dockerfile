# Copyright 2024 Deutsche Telekom AG
#
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.24-alpine AS build

ARG HTTP_PROXY
ARG HTTPS_PROXY

ENV HTTP_PROXY=$HTTP_PROXY
ENV HTTPS_PROXY=$HTTPS_PROXY

WORKDIR /build
COPY . .
RUN apk add --no-cache build-base
RUN apk add --no-cache --update ca-certificates
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s -extldflags=-static" -o ./out/quasar

FROM scratch

COPY --from=build /build/out/quasar .
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

ENTRYPOINT ["./quasar"]
CMD ["run"]