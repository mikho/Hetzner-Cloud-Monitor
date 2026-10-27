# Build the agent from source for both server architectures. No downloads
# at install time: the dashboard uploads these binaries over SSH.
FROM golang:1.23-alpine AS agent
WORKDIR /src
COPY agent ./
ARG AGENT_VERSION=0.1.0
RUN for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=${AGENT_VERSION}" -o /out/hdash-agent-linux-$arch . ; \
    done

FROM node:22-alpine
RUN apk add --no-cache openssh-client-default
WORKDIR /app
COPY server.js ./
COPY lib ./lib
COPY public ./public
COPY --from=agent /out ./agent-bin
ENV NODE_ENV=production PORT=3000 DATA_DIR=/data
USER node
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:3000/healthz || exit 1
CMD ["node", "server.js"]
