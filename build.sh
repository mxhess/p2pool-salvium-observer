#!/bin/bash
# Build script for p2pool-salvium-observer components

set -e

BINDIR="bin"
mkdir -p "$BINDIR"

echo "Building p2pool-salvium-observer..."

# Build daemon (clean - uses only p2pool package)
echo "  -> daemon"
go build -o "$BINDIR/daemon" ./cmd/daemon/

# Build API (clean - uses only p2pool package)
echo "  -> api"
go build -o "$BINDIR/api" ./cmd/api/

echo ""
echo "Build complete. Binaries in $BINDIR/"
ls -lh "$BINDIR/"

echo ""
