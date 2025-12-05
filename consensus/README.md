# P2Pool Consensus

This repository contains a consensus-compatible reimplementation of a P2Pool internals for [Monero P2Pool](https://github.com/SChernykh/p2pool) decentralized pool.

Other general tools to work with Monero cryptography are also included.

You may be looking for [P2Pool Observer](https://git.gammaspectra.live/P2Pool/observer) instead.

## Reporting issues

You can give feedback or report / discuss issues on:
* [The issue tracker on git.gammaspectra.live/P2Pool/consensus](https://git.gammaspectra.live/P2Pool/consensus/issues?state=open)
* Via IRC on [#p2pool-log@libera.chat](ircs://irc.libera.chat/#p2pool-log), or via [Matrix](https://matrix.to/#/#p2pool-log:monero.social)

## Donations
This project is provided for everyone to use, for free, as a hobby project. Any support is appreciated.

Donate to support this project, its development, and running the Observer Instances on [4AeEwC2Uik2Zv4uooAUWjQb2ZvcLDBmLXN4rzSn3wjBoY8EKfNkSUqeg5PxcnWTwB1b2V39PDwU9gaNE5SnxSQPYQyoQtr7](monero:4AeEwC2Uik2Zv4uooAUWjQb2ZvcLDBmLXN4rzSn3wjBoY8EKfNkSUqeg5PxcnWTwB1b2V39PDwU9gaNE5SnxSQPYQyoQtr7?tx_description=P2Pool.Observer)

You can also use the OpenAlias `p2pool.observer` directly on the GUI.

### Development notes

This library supports both [Golang RandomX library](https://git.gammaspectra.live/P2Pool/go-randomx) and the [C++ RandomX counterpart](https://github.com/tevador/RandomX).

By default, the Golang library will be used. You can enable the C++ library if by using CGO and the compile tag `enable_randomx_library` and have it installed via the command below:
```bash
$ git clone --depth 1 --branch master https://github.com/tevador/RandomX.git /tmp/RandomX && cd /tmp/RandomX && \
    mkdir build && cd build && \
    cmake .. -DCMAKE_BUILD_TYPE=Release -D CMAKE_INSTALL_PREFIX:PATH=/usr && \
    make -j$(nproc) && \
    sudo make install && \
    cd ../ && \
    rm -rf /tmp/RandomX
```