# Hard forks within P2Pool (and other changes)

Whenever breaking changes or new features that require so for improvements or security, P2Pool will hard fork.

This means that non-upgraded clients will stay behind and split away from newer clients.

Whenever a Monero network upgrade occurs, a hard fork occurs regardless, where P2Pool can also implement upgrades at the same time.

Some features might change but not be strictly enforced until later.

## P2Pool v2.2+ / Monero v15/v16

Version v2.x supports Monero v15/v16 hard fork. v2.2 was required to work with it due to other changes. 

* v2.2 included [14bbf9 Enforce deterministic tx keys starting from v15](https://github.com/SChernykh/p2pool/commit/14bbf96feb50e962e6a89433eab74a78b331219f)
  * Keys generated with these versions before the hard fork were also generated deterministically but not enforced.
  * Deterministic transaction private key enforcement started on major version >= v15.
  * This change prevents burning of Coinbase outputs by attackers, as reusing a transaction key could have miner outputs also reuse their ephemeral public key.
  * The deterministic private key is generated via this method:
    * ```
        seed = share.WalletSpendPublicKey
        entropy = keccak("tx_secret_key" | seed | previous_monero_id)
        private key = deterministic_scalar(entropy)
      ```

#### Coinbase output burning attack
Before deterministic transaction private keys an attacker could reuse a private key to cause outputs to go to the same ephemeral public key for miner payouts, given their output index is equal.

Inputs with the same ephemeral public key cannot be used and only one can be spent.

The issue was fixed by generating the private key via deterministic random generator, where the input is known to miners and can be verified, and was enforced on hard fork.

### P2Pool v2.6+
Version v2.6 implemented stricter checks on lagging behind or outdated Monero blocks, causing those bad shares to get ignored for inclusion.


#### Chain split attack
Commit [45660e Show wallet address in error messages and status](https://github.com/SChernykh/p2pool/commit/45660e3d9612428eb7855f64f103b52088a214ed) on Nov 3, 2022 fixed an attack that could cause a chain split.

This commit appears unrelated but the hidden fix is in lines of `src/wallet.h` for `bool operator<(const Wallet& w)` and `bool operator==(const Wallet& w) const`.
```diff
-	FORCEINLINE bool operator<(const Wallet& w) const { return m_spendPublicKey < w.m_spendPublicKey; }
-	FORCEINLINE bool operator==(const Wallet& w) const { return m_spendPublicKey == w.m_spendPublicKey; }
+	FORCEINLINE bool operator<(const Wallet& w) const { return (m_spendPublicKey < w.m_spendPublicKey) || ((m_spendPublicKey == w.m_spendPublicKey) && (m_viewPublicKey < w.m_viewPublicKey)); }
+	FORCEINLINE bool operator==(const Wallet& w) const { return (m_spendPublicKey == w.m_spendPublicKey) && (m_viewPublicKey == w.m_viewPublicKey); }
```

Before this commit, share outputs were ordered using only the miner public spend key via `std::sort`.

An attacker could pick a target's public spend key and fill a random view key.
At best, the attack would make coinbase outputs unspendable by the affected miner depending on where sort order happened. 
At worst, given undefined sort behavior, it would cause a chain split as sort order would be undefined for this entry.

This issue was found as part of the effort to replicate P2Pool consensus in Golang, for P2Pool Observer. It was disclosed privately to sech1 via IRC on Nov 1, 2022. 

#### Invalid broadcast race condition
Commit [255d31](https://github.com/SChernykh/p2pool/commit/255d312ae0d03171dd31a2fab5fbb87e508c4024) on Nov 2, 2022 fixed an attack that could target specific miners and get them temporarily banned from the network.

Before this commit, whether a block had been seen or not was done based on its template id.
However, blocks with different nonces or extra nonces have the same template id.

An attacker could see a specific miner share and quicker than them change the nonce/extra nonce, and broadcast it to other peers faster than the original miner.
Regardless, the attacker node would be banned for 10 minutes. If they won the race, the template id would be labeled invalid and not be able to be included by other peers.
Whenever peers broadcasted that template id, they would be banned in turn.

This was fixed by introducing a _Full Id_ composed of the template id, nonce, and extra nonce together to check for seen blocks.

This issue was found as part of the effort to replicate P2Pool consensus in Golang, for P2Pool Observer. It was disclosed privately to sech1 via IRC on Nov 2, 2022.

#### Other noteworthy consensus or verification issues
* Genesis block does not validate the miner coinbase outputs properly, but this only affects the miner itself willingly doing this.
* A miner with about 10% total hashrate for a given pool could break difficulty calculation by picking their timestamp as `current time + 2^32`, and overflow difficulty into minimum. This was fixed by making such calculations occur in 64-bit mode, plus additional verification for timestamps.
* A miner could broadcast shares with the wrong Monero major version for current hardfork. This was fixed by verifying the new blocks match current expected Monero hardfork version.

## P2Pool v3.0+

Version v3.0 implemented hard fork changes for allowing dynamic PPLNS window and improvements to deterministic keys via a new share version.

* Share Version v2 was introduced:
  * Transaction private key field now changed to transaction private key seed.
  * Extra data added to side data. Contains extra nonce, random number, software id and software version for the share miner.
  * Coinbase miner outputs are shuffled using a deterministic random method, using the private key seed.
    * This reduces how likely miner outputs on Monero blocks can be linked each other for private pools
* The deterministic seed generation changed to:
  * ```
      seed = keccak("tx_key_seed\0" | share.Main.SidechainHashingBlob() | share.Side.Blob())
    ```
  * The deterministic seed changes only when the previous Monero id in the parent share is different than current Monero id. If so, the parent share seed is used.
  * For the genesis share, the consensus id is used as seed.
* Dynamic PPLNS window introduced, targeting 2 Monero blocks found per window on average (`mainchain difficulty * 2`)
  * If greater than PPLNS window size, use that instead

### P2Pool v3.3+

* P2Pool could fail to sync if uncles of depth 3 were present at a certain depth in the chain.
  * Fixed in [b49808 SideChain: fixed a rare sync bug](https://github.com/SChernykh/p2pool/commit/b4980843884d01fd1070710b2b7c08f5f6faca91)
  * Go consensus [c43862 Match consensus sync on high depth uncles as they leave outer PPLNS window](https://git.gammaspectra.live/P2Pool/consensus/commit/c438622558adf71698335af7a3eca818c540ffe8) and [61cfc4 Match deep block behavior in missed spot](https://git.gammaspectra.live/P2Pool/consensus/commit/61cfc4478cb4b97e85ef836382970e77257d769e)



## P2Pool v4.0+

Version v4.0 implemented hard fork changes to allow merge mining via P2Pool.

* Merge mining tag was [previously encoded wrongly](https://github.com/SChernykh/p2pool/issues/249). This has now been fixed.
* Merge mining tag now includes the root hash of the merge mining tree.
* Side data contains the merkle proof to verify the template id is included under the root hash of the tree.
* Side data also has a vector of chain id and arbitrary data pairs for any necessary data to be included for other chains in-template, for future proofing.
* On pruned blocks, template id is included within the coinbase transaction pruned data.
* The Monero block major/minor version is encoded as a varint, but their values cannot exceed 256. P2Pool encoded these always as bytes. A check was added now to prevent any minor versions greater than 128 being encoded. 
  * Fixed in [4bc0b5 Block parser: sanity check minor version](https://github.com/SChernykh/p2pool/commit/4bc0b559cbb2f01a074e9168d16a09b8e1deff98****)
  * Go consensus [d0f153 fuzz: fix minor version on main block higher than max varint size for one byte](https://git.gammaspectra.live/P2Pool/consensus/commit/d0f1537fbab4aeb97655192de8c66efb9d56f965)
* Side difficulty and side height now have limits to prevent overflows.
  * Reported possible issue privately, with full extent still undisclosed.

### P2Pool v4.9+

A testing and fuzzing (random coverage testing) campaign was initiated on Go consensus for Observer.
Many minor and major issues were found that only affected Go consensus, but several affected P2Pool as well and were reported.  

* P2Pool could fail to sync if a certain order of blocks was received.
  * This issue was found by tests in Go consensus for Observer. It was disclosed to sech1 via IRC on Jun 20th, 2025.
  * Fixed in [c42132 SideChain: fixed a synchronization blocker bug](https://github.com/SChernykh/p2pool/commit/c421324b7362f118be17e4688922f7fc472f35af)
  * Go consensus [adee5d fix sync blocker bug on sidechain](https://git.gammaspectra.live/P2Pool/consensus/commit/adee5d8340d30b0a3b5f72e52f742774dd4b11c4)
* P2Pool would read non-canonical encodings of VarInt causing a different binary encoding of blocks after a decode/encode roundtrip.
  * This issue was found by fuzzing Go consensus for Observer. It was disclosed privately to sech1 via IRC on Jun 23th, 2025.
  * This bug could allow an attacker to send valid blocks to other peers, which would validate. However, as soon as this node would try to broadcast this block, the encoded data would be invalid and be banned by the rest of its peers instantly.
  * Affected nodes could not rejoin or reconnect as this block was broadcasted instantly. Only once enough time has passed in the pool and Monero, the node would re-sync to a newer chain.
  * On older chains that do not merge mine, a denegation of service is also able to be done to fill RAM of the target node. This is no longer possible.
  * Fixed in [55eef3 Added more checks to readVarint](https://github.com/SChernykh/p2pool/commit/55eef3c5e9d21f8bc53b7be248a893fa23dfa612)
  * Go consensus [73f5cc fuzz: ensure only normalized zeros on varints are read, or fail](https://git.gammaspectra.live/P2Pool/consensus/commit/73f5ccc654cff1b77fafb129ae2df8f46e77057c)
* P2Pool would decode a TxOutput as 64-bit VarInt, but clamp it to 56 bits, causing a different binary encoding of blocks after a decode/encode roundtrip.
  * This issue was found by newly added fuzzing on P2Pool code. It was shared privately via IRC on Jun 23th, 2025.
  * Fixed in [1b05f1 Block parser: stricter checks](https://github.com/SChernykh/p2pool/commit/1b05f1e7504f55e297791e8d39135b40e63f8c55)
  * Go consensus [406e4a verify maximum tx output reward allowed in a pool block, up to 56 bits](https://git.gammaspectra.live/P2Pool/consensus/commit/406e4a19d992663bbb92ad371155c9d1a1ef4901)