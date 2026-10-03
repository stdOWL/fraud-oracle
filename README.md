# fraud-oracle

Off-chain fraud detection in Go, published on-chain through the Chainlink Runtime Environment (CRE).

A smart contract cannot ask an API whether an address is suspicious. This repo scores addresses off-chain from indexed ERC20 transfers, then lets a decentralized oracle network (DON) fetch that score, agree on it, sign it, and write it to a `FraudRegistry` contract that any other contract can query with `isFlagged(address)`. Demonstrated end to end on Sepolia with `cre workflow simulate --broadcast`.

Write-up: [docs/medium.md](docs/medium.md).

## Architecture

![Architecture](docs/architecture.png)

Analysis stays off-chain because graph traversal over 50,000 blocks of transfers does not belong in a WASM workflow with a five-second HTTP budget. Publication goes through CRE because every DON node calls `/score` independently and only an answer they agree on gets signed and written. The service's job is to be deterministic for a given address and block height so that agreement is possible.

```
service/     Go: eth_getLogs indexer with Postgres checkpoint, rule engine, GET /score, /metrics
contracts/   Foundry: FraudRegistry.sol on top of Chainlink's ReceiverTemplate, deploy script, tests
workflow/    CRE project (Go, compiled to wasip1): Transfer log trigger -> HTTP /score -> EVM write
scripts/     peel-chain-demo.sh: manufactures a flaggable transfer chain on Sepolia
```

Three Go modules on purpose: the CRE CLI expects its project root to be the module and compiles it for WASM; the service must not depend on the CRE SDK.

## Quick start

Requirements: Go 1.25.3+ (CRE SDK), Docker, [Foundry](https://getfoundry.sh), [CRE CLI](https://docs.chain.link/cre) v1.35+ with `cre login`, a Sepolia RPC URL, a throwaway wallet with Sepolia ETH.

```bash
git clone --recursive https://github.com/stdOWL/fraud-oracle && cd fraud-oracle
cp .env.example .env            # fill SEPOLIA_RPC_URL, TOKEN_ADDRESS, DEPLOYER_PRIVATE_KEY
cp .env.example workflow/.env   # CRE reads this one: SEPOLIA_RPC_URL, CRE_ETH_PRIVATE_KEY (no 0x)
docker compose up -d --wait
```

Index and serve:

```bash
set -a && source .env && set +a
(cd service && go run ./cmd/indexer)     # backfills head-50000..head, then follows the head
(cd service && go run ./cmd/api)         # :8080
curl "localhost:8080/score?address=0x0330070fd38ec3bb94f58fa55d40368271e9e54a"
```

That address is on the OFAC list and scores 100. Restart the indexer at any point; it resumes from its checkpoint.

Deploy the registry (Sepolia gas) with the simulation forwarder, then put the printed address into `workflow/fraud-flagger/config.staging.json` as `registryAddress` and the token into `tokenAddress`:

```bash
(cd contracts && forge script script/Deploy.s.sol --rpc-url sepolia --broadcast)
```

Simulate on any `Transfer` of the token (dry run, no gas), then for real:

```bash
cd workflow
cre workflow simulate fraud-flagger --target staging-settings \
  --evm-tx-hash 0x<transfer tx> --evm-event-index <log position in that tx>
cre workflow simulate fraud-flagger --target staging-settings --broadcast \
  --evm-tx-hash 0x<transfer tx> --evm-event-index <log position in that tx>
```

Sepolia LINK has no peel chains, so `scripts/peel-chain-demo.sh` makes one (four burner wallets, ~0.02 ETH + 1 LINK). Wait for finality plus 64 blocks, then trigger on the last hop's transaction.

## Demo run

| | |
|---|---|
| Token | Sepolia LINK `0x779877A7B0D9E8603169DdbD7836e478b4624789` |
| FraudRegistry | [`0x242a1Fa96000A298d1C37FcD1617e523bcF16416`](https://sepolia.etherscan.io/address/0x242a1fa96000a298d1c37fcd1617e523bcf16416) (Sourcify verified) |
| Trigger | hop 4 of a manufactured peel chain, `0x499b24d8…abde9` |
| Flag tx | [`0x1e55b220…8819`](https://sepolia.etherscan.io/tx/0x1e55b220a1164f6c690a0d6c01abbcbef8966ca50d5a358d9b5be554ab918819) via MockKeystoneForwarder |
| Result | `isFlagged(0xA89C…7ffc)` = `true`, `getFlag` = `(50, 1, 1790983428)` |

## The rules

**Peel chain.** Funds move A → B → C → D, each receiver fresh (no activity before the hop), each forwarding at least 90% of what it received within 1,000 blocks. Three hops score 30, each extra hop +20, capped at 100. The subject is usually a mule mid-chain, so the rule walks back to the origin first; every address on the chain gets the same score and evidence. Limits: greedy along the largest outgoing transfer, misses chains that split; the pattern comes from UTXO forensics and is a first approximation on an account-based chain.

**Sanctions proximity.** Undirected breadth-first search, up to three hops from the subject, against the EVM addresses on the OFAC SDN list. On the list: 100. One hop: 80. Two: 50. Three: 25. Evidence is the transfer path. Limits: undirected proximity can be griefed by dusting from a listed address; inbound and outbound taint are weighted the same; flags never expire.

The list is regenerated from treasury.gov's `sdn.xml` by `go run ./cmd/ofac` and pinned at commit time (`service/testdata/sanctions.json`, 124 addresses as of 2026-10-03). The service never fetches at runtime: every DON node must score against the same list.

## API

`GET /score?address=0x…&block=N`

```json
{"address":"0x…","score":50,"ruleBitmask":1,
 "rules":[{"name":"peel_chain","evidence":["0x…","0x…"]}],
 "indexedThroughBlock":11832051}
```

`block` pins the graph to a height so nodes with different indexer lag return identical answers; it is clamped to the checkpoint and the height used is reported. `ruleBitmask`: bit 0 peel chain, bit 1 sanctions. `503` until the indexer has a checkpoint. `/metrics` is Prometheus text: score latency histogram, blocks indexed, head lag. `/healthz` returns 204.

## Tests

```bash
docker compose exec postgres psql -U fraud -c 'CREATE DATABASE fraud_test'
(cd service && TEST_DATABASE_URL='postgres://fraud:fraud@localhost:5432/fraud_test?sslmode=disable' go test ./...)
(cd contracts && forge test)
(cd workflow && go test ./...)
```

Rules are table-driven on synthetic graphs. Indexer resume uses a fake RPC and a real Postgres (tests truncate the database they point at). The workflow tests use the CRE SDK's mock HTTP and EVM capabilities.

## What I hit

- A CRE workflow cannot call an arbitrary contract function. Writes are DON-signed reports delivered by `KeystoneForwarder` to `onReport`; the contract inherits `ReceiverTemplate`. `flag(FlagReport)` exists only so `cre generate-bindings` emits `WriteReportFromFlagReport`, and always reverts.
- Simulation writes through a `MockKeystoneForwarder` that passes no workflow metadata. Enable workflow-ID or author checks in the contract and simulation reverts.
- The indexer and the CRE trigger read the same log with nothing ordering them. The service clamps to what it has indexed; a flag can land one transfer late, never wrong. See the write-up.
- OFAC tags digital-currency addresses per asset, not per chain. Filtering on `ETH` alone silently dropped four sanctioned EVM addresses listed under `USDT`, `USDC`, `ARB`, `BSC`, `ETC`.
- The CRE docs do not say whether the HTTP capability can reach private IP ranges. The node source does: requests go through a Gateway whose client is built on `doyensec/safeurl`, which blocks private, loopback and link-local ranges by default and checks after DNS resolution.
- Three bugs appeared only against the real chain: the API loaded a two-hop graph while the rules look further; the peel rule scored only chain origins, not mules; two indexer processes racing on one range froze the checkpoint. All have regression tests now.

## Not built

- Deployment to a DON. No deploy access, and a deployed DON cannot reach `localhost`; the demo is `simulate --broadcast` on one node, which proves the plumbing, not the Byzantine consensus.
- Multiple scoring sources with a median across them. One source means the DON proves consistency, not correctness.
- Waiting for the indexer to reach the trigger block before scoring.
- Inbound/outbound taint weighting, flag expiry or revocation, any third rule.
- Live sanctions refresh, reorg handling beyond indexing finalized blocks only, mainnet, other chains, dashboards, alerts.
