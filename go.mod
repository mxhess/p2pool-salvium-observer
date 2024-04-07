module git.gammaspectra.live/P2Pool/observer

go 1.22

require (
	git.gammaspectra.live/P2Pool/consensus/v3 v3.3.0
	git.gammaspectra.live/P2Pool/observer-cmd-utils v0.0.0-20240407183636-4caa6b12bcd8
	github.com/goccy/go-json v0.10.2
	github.com/gorilla/mux v1.8.1
	github.com/mazznoer/colorgrad v0.9.1
	github.com/tmthrgd/go-hex v0.0.0-20190904060850-447a3041c3bc
	github.com/valyala/quicktemplate v1.7.0
	nhooyr.io/websocket v1.8.11
)

require (
	git.gammaspectra.live/P2Pool/edwards25519 v0.0.0-20240405085108-e2f706cb5c00 // indirect
	git.gammaspectra.live/P2Pool/go-randomx v0.0.0-20221027085532-f46adfce03a7 // indirect
	git.gammaspectra.live/P2Pool/monero-base58 v1.0.0 // indirect
	git.gammaspectra.live/P2Pool/randomx-go-bindings v0.0.0-20230514082649-9c5f18cd5a71 // indirect
	git.gammaspectra.live/P2Pool/sha3 v0.17.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/dolthub/maphash v0.1.0 // indirect
	github.com/dolthub/swiss v0.2.2-0.20240312182618-f4b2babd2bc1 // indirect
	github.com/floatdrop/lru v1.3.0 // indirect
	github.com/jxskiss/base62 v1.1.0 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mazznoer/csscolorparser v0.1.3 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	golang.org/x/crypto v0.22.0 // indirect
	golang.org/x/sys v0.19.0 // indirect
	lukechampine.com/uint128 v1.3.0 // indirect
)

replace github.com/goccy/go-json => github.com/WeebDataHoarder/go-json v0.0.0-20230730135821-d8f6463bb887

replace github.com/go-zeromq/zmq4 => git.gammaspectra.live/P2Pool/zmq4 v0.16.1-0.20240407153747-7f7d531f586e
