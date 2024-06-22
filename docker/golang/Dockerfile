FROM golang:1.22-alpine3.19 AS builder


ENV CFLAGS="-march=native -Ofast"
ENV CXXFLAGS="-march=native -Ofast"
ENV LDFLAGS="-flto"
ENV CGO_CFLAGS="-march=native -Ofast"

RUN apk update && apk add --no-cache \
    git gcc g++ musl-dev bash autoconf automake cmake make libtool gettext

RUN git clone --depth 1 --branch v1.2.1 https://github.com/tevador/RandomX.git /tmp/RandomX && cd /tmp/RandomX && \
    mkdir build && cd build && \
    cmake .. -DCMAKE_BUILD_TYPE=Release -D CMAKE_INSTALL_PREFIX:PATH=/usr && \
    make -j$(nproc) && \
    make install && \
    cd ../ && \
    rm -rf /tmp/RandomX



WORKDIR /usr/src/p2pool

ARG GOPROXY=""
ENV GOPROXY=${GOPROXY}
ARG GOAMD64=""
ENV GOAMD64=${GOAMD64}

COPY go.mod go.sum ./
RUN go mod download -x && go mod verify

COPY . .

ENV GOEXPERIMENT=newinliner

# Use same tag as consensus protocol by default
ARG P2POOL_CONSENSUS_PACKAGE=git.gammaspectra.live/P2Pool/consensus/v4
ARG P2POOL_PACKAGE=git.gammaspectra.live/P2Pool/go-p2pool/v4
RUN go get "${P2POOL_PACKAGE}@$(go list -m "${P2POOL_CONSENSUS_PACKAGE}" | awk '{print $2}')"
RUN go build -buildvcs=false -trimpath -v -pgo=auto -o /usr/bin/p2pool "${P2POOL_PACKAGE}"


RUN go run github.com/valyala/quicktemplate/qtc@v1.7.0

RUN go build -buildvcs=false -trimpath -v -pgo=auto -o /usr/bin/api git.gammaspectra.live/P2Pool/observer/cmd/api
RUN go build -buildvcs=false -trimpath -v -pgo=auto -o /usr/bin/daemon git.gammaspectra.live/P2Pool/observer/cmd/daemon
RUN go build -buildvcs=false -trimpath -v -pgo=auto -o /usr/bin/web git.gammaspectra.live/P2Pool/observer/cmd/web

FROM alpine:3.19

RUN apk update && apk add --no-cache \
    libstdc++ libgcc

ARG BUILD_BINARY

COPY --from=builder /usr/bin/${BUILD_BINARY} /usr/bin/${BUILD_BINARY}

WORKDIR /data