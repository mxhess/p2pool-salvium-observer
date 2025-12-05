# p2pool test data

Test data from upstream p2pool.

File checksums generated via `$ sha256sum --check *.dat *.gz *.txt` are stored under [testdata.sha256](testdata.sha256)

## Sidechain dumps

### _v1_sidechain_dump.dat_
Dump of the blocks from P2Pool Main, including heights [4952872](https://p2pool.observer/share/4952872) to [522805](https://p2pool.observer/share/522805).

This was done before P2Pool v2 hardfork and is not compressed.

### _v1_sidechain_dump_mini.dat_
Dump of the blocks from P2Pool Mini, including heights [2420029](https://mini.p2pool.observer/share/2420029) to [2424349](https://mini.p2pool.observer/share/2424349).

This was done before P2Pool v2 hardfork and is not compressed.

Due to a bug before commit on [P2Pool b498084388](https://github.com/SChernykh/p2pool/commit/b4980843884d01fd1070710b2b7c08f5f6faca91) / [consensus c438622558](https://git.gammaspectra.live/P2Pool/consensus/commit/c438622558adf71698335af7a3eca818c540ffe8) several blocks are missing to finish a proper SideChain sync.

These blocks are included as `old_sidechain_dump_mini_2420027.dat` and `old_sidechain_dump_mini_2420028.dat` as obtained from Observer, and are required to do proper sync.

### _v1_mainnet_test2_block.dat_
Dump of the block height [53450](https://p2pool.observer/share/53450) from P2Pool Main.

This was done before P2Pool v2 hardfork.

### _v2_crypto_tests.txt_
Several cryptography tests to verify derivations and transaction key generation

### _v2_sidechain_dump.dat.gz_
Dump of the blocks from P2Pool Main, including heights [4952872](https://p2pool.observer/share/4952872) to [4957203](https://p2pool.observer/share/4957203).

This was done after P2Pool v2 hardfork and is compressed.

### _v2_sidechain_dump_mini.dat.gz_
Dump of the blocks from P2Pool Mini, including heights [4410115](https://mini.p2pool.observer/share/4410115) to [4414446](https://mini.p2pool.observer/share/4414446).

This was done after P2Pool v2 hardfork and is compressed.

### _v2_block.dat_
Dump of the block height [4674483](https://p2pool.observer/share/4674483) from P2Pool Main.

This was done after P2Pool v2 hardfork.

### _v4_sidechain_dump.dat.gz_
Dump of the blocks from P2Pool Main, including heights [-](https://p2pool.observer/share/-) to [9443762](https://p2pool.observer/share/9443762).

This was done after P2Pool v4 hardfork and is compressed.

### _v4_sidechain_dump_mini.dat.gz_
Dump of the blocks from P2Pool Mini, including heights [-](https://mini.p2pool.observer/share/-) to [8912067](https://mini.p2pool.observer/share/8912067).

This was done after P2Pool v4 hardfork and is compressed.

### _v4_sidechain_dump_nano.dat.gz_
Dump of the blocks from P2Pool Nano, including heights [-](https://mini.p2pool.observer/share/-) to [116651](https://nano.p2pool.observer/share/116651).

### _v4_block.dat_
Dump of the block height [9443384](https://p2pool.observer/share/9443384) from P2Pool Main.

This was done after P2Pool v4 hardfork.