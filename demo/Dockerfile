# Records demo/demo.gif with VHS (https://github.com/charmbracelet/vhs).
# From the repository root:
#   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o demo/unasked ./cmd/unasked   # or GOARCH=amd64
#   docker build -t unasked-vhs demo
#   docker run --rm -v "$PWD/demo":/work -w /work unasked-vhs demo.tape
FROM ghcr.io/charmbracelet/vhs
RUN apt-get update && apt-get install -y --no-install-recommends git && rm -rf /var/lib/apt/lists/*
COPY unasked /usr/local/bin/unasked
