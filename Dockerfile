# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.27.2-alpine3.24
ARG NODE_IMAGE=node:24.22.2-alpine3.24
ARG RUNTIME_IMAGE=alpine:3.24

# Optional web build: only runs when web/package.json exists.
FROM ${NODE_IMAGE} AS web
WORKDIR /web
COPY web/ ./
RUN if [ -f package.json ]; then \
      (npm ci --no-audit --no-fund || npm install --no-audit --no-fund) && npm run build; \
    fi && mkdir -p /web/build

FROM ${GO_IMAGE} AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed web build if present (index.html), else keep the placeholder page.
COPY --from=web /web/build/ /tmp/webbuild/
RUN if [ -f /tmp/webbuild/index.html ]; then rm -rf internal/server/static && cp -r /tmp/webbuild internal/server/static; fi
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/BxnnyG/ansible-control-plane/internal/version.Version=${VERSION} -X github.com/BxnnyG/ansible-control-plane/internal/version.Commit=${COMMIT}" \
      -o /out/acp ./cmd/acp

FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache git ca-certificates openssh-client-default \
 && adduser -D -u 10001 -h /home/acp acp
COPY --from=build /out/acp /usr/local/bin/acp
USER acp
ENV HOME=/home/acp
EXPOSE 8080 8443
ENTRYPOINT ["acp"]
CMD ["serve"]
