# Builds unasked into a small image with git.
#   docker build -t unasked .
#   docker run --rm -v "$PWD":/repo unasked --since origin/main
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /unasked ./cmd/unasked

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && git config --system --add safe.directory '*'
COPY --from=build /unasked /usr/local/bin/unasked
WORKDIR /repo
ENTRYPOINT ["unasked"]
