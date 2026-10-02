# Building an On-chain Fraud Oracle with Go and Chainlink CRE

*A weekend project: off-chain fraud detection in Go, published on-chain through a decentralized oracle network. Repo: github.com/<user>/fraud-oracle. Everything described here is in the repo and runs; the limits are stated where they apply.*

## Why put this on-chain at all

A fraud score is an opinion. Chainalysis has one, TRM has one, my Go service has one. Each lives behind an HTTP endpoint, each is a single company's word, and each can change its answer between two calls. That is fine for a compliance dashboard. It is not fine for a smart contract.

A contract cannot call an API. It cannot retry, cannot ask a second opinion, cannot tell a stale answer from a fresh one. Whatever it reads from storage, it acts on: block the withdrawal, reject the deposit, freeze the position. If that storage was written by one server, the protocol has outsourced its security to that server's uptime, honesty, and operator's private key.

This is the oracle problem, and the crypto industry has already paid for the lesson several times. In 2019 Synthetix's own price feed averaged three sources; one source returned the Korean won at 1000x and the average followed it, and a trading bot extracted roughly a billion dollars of synthetic assets in minutes before the trades were reversed. In 2022 Chainlink paused the LUNA feed during the collapse; Venus Protocol kept reading the last value, never checked how old it was, and lost $11 million. In 2025 Moonwell lost money three times to a feed returning nonsense for a wrapped token that the protocol accepted without a sanity check.

Three failures, one shape: a single off-chain fact crossed into a contract without anyone independently checking it on the way in.

An on-chain fraud flag should not be another instance of that shape. The value of putting the flag on-chain is not that it is on-chain. Storage is cheap. The value is that **the act of writing it can be made verifiable**: multiple independent parties fetch the same answer, agree, and sign, and the contract accepts only their joint signature. Then the flag is a fact other contracts can build on, not a rumour one server posted.

That is what this project builds. The analysis stays off-chain, because graph traversal over fifty thousand blocks of transfers has no business inside a smart contract. The publication goes through the Chainlink Runtime Environment, because a decentralized oracle network turns my single API into something a contract can trust more than me.

## What it does

One ERC20 token on Sepolia. For every `Transfer`, score the sender and the receiver 0 to 100 with two rules, and if either scores at or above a threshold, record it in a `FraudRegistry` contract that any other contract can query with `isFlagged(address)`.

```
 Sepolia ERC20 Transfer logs
          │
          ▼
 ┌─────────────────────┐        ┌──────────────────────────────┐
 │  service/ (Go)      │        │  workflow/ (CRE, Go → WASM)  │
 │  eth_getLogs indexer│        │  trigger: Transfer log       │
 │  Postgres checkpoint│◄───────│  HTTP /score (DON consensus) │
 │  rule engine        │  GET   │  if score ≥ threshold        │
 │  GET /score         │        │  EVM write report            │
 └─────────────────────┘        └──────────────┬───────────────┘
                                               │ KeystoneForwarder
                                               ▼
                                 ┌──────────────────────────────┐
                                 │ contracts/FraudRegistry.sol  │
                                 │ onReport → store flag        │
                                 │ isFlagged(address) view      │
                                 └──────────────────────────────┘
```

Step by step, per transfer:

| # | Where | Step |
|---|---|---|
| 1 | on-chain | `Transfer(from, to, value)` log emitted, block finalized |
| 2 | off-chain | Indexer stores the log in Postgres |
| 3 | off-chain | CRE log trigger starts the workflow on every DON node |
| 4 | off-chain | Each node calls `GET /score?address=X&block=N` on its own |
| 5 | off-chain | Service runs the rules, returns score and evidence |
| 6 | off-chain | Nodes compare answers; identical ones get a DON signature |
| 7 | on-chain | `KeystoneForwarder` checks the signatures, calls `onReport` |
| 8 | on-chain | `FraudRegistry` decodes the report, emits `Flagged` |

Only 1, 7 and 8 cost gas. Step 6 is the one that matters: it is where one server's opinion becomes a verifiable fact.

## The off-chain service

### Indexer

Ethereum nodes answer "which logs did this contract emit in these blocks". They do not answer "who did this address send money to". For the second question you need your own table with an index on the address columns, and the indexer's whole job is to build it.

It pulls `Transfer` logs with `eth_getLogs` in 2,000-block ranges, eight ranges in flight, and writes them to Postgres keyed on `(tx_hash, log_index)` so a repeated insert is a no-op. Each range commits its rows together with a `range_done` marker in one transaction. A coordinator advances a single-row `checkpoint` only over contiguous completed ranges, so if range 5 finishes before range 3, the checkpoint waits at 2. Kill it, restart it, and it resumes from the checkpoint without re-fetching anything. The test for this uses a fake RPC that fails after two ranges, then confirms the second run makes exactly four more calls, not six.

Two choices came from running my own Ethereum and BSC nodes: index only up to `head - 64` so reorgs never reach the table, and split a range in half when the provider says the result is too large, because every provider has a different limit and none of them document it in the error.

### Rules

Each rule is one Go type behind one interface, evaluated against an in-memory graph built per request from the address's two-hop neighbourhood. The graph is sorted by `(block, log_index)` before any rule touches it, so the same database state always produces the same answer. That determinism is not tidiness; it is a requirement, and the reason is in the next section.

**Peel chain.** Funds move A → B → C → D, each receiver fresh (no activity before the hop), each forwarding at least 90% of what it received within 1,000 blocks. Three hops score 30, each extra hop adds 20, capped at 100. A round trip back to an earlier address is not a peel and terminates the chain. The limit is plain: the rule follows the largest outgoing transfer greedily and will miss a chain that splits.

**Sanctions proximity.** Breadth-first search, undirected, up to three hops from the address, against the 120 Ethereum addresses on the OFAC SDN list. The address itself on the list scores 100; one hop away 80, two 50, three 25. Evidence is the transfer path to the nearest listed address.

The list is the real one. `cmd/ofac` downloads `sdn.xml` from treasury.gov, extracts the entries tagged `Digital Currency Address - ETH`, and writes them to a JSON file the service loads at startup. I cross-checked it against a community mirror: identical, 120 of 120. What is on it: Lazarus Group wallets, Garantex, SUEX, a dozen addresses belonging to an Iranian ransomware operator, fentanyl precursor suppliers. What is not on it: the Tornado Cash contracts, delisted in March 2025, which my first hand-written fixture still had.

Why a static file and not Chainalysis's free screening API? Because the rule is not "is this address listed", it is "is this address near a listed one", and one scoring call can touch thousands of addresses across three hops. A per-address API at 30 to 100 requests an hour cannot serve that, and its answers can differ between the nodes that will be calling my service in parallel. A list pinned at commit time is fast and identical everywhere. It is also stale the moment OFAC updates; regenerating it is one command, and a deployed version would do that on a schedule.

### API

`GET /score?address=0x…&block=N` returns

```json
{"address":"0x…","score":80,"ruleBitmask":2,
 "rules":[{"name":"sanctions_proximity","evidence":["0x…"]}],
 "indexedThroughBlock":9123456}
```

The `block` parameter is the piece that makes the whole design work. The DON nodes do not call my service at the same instant, and my indexer is not at the same height on every call. If each node scored at "whatever is indexed right now", two nodes could get two different answers for the same address, consensus would fail, and nothing would ever be written. So the workflow passes the block number of the transfer that triggered it, the service scores at exactly that height, and every node sees the same graph. `indexedThroughBlock` reports the actual height used and is explicitly excluded from consensus.

The handler times out at four seconds because CRE's HTTP capability gives up at five by default. `/metrics` exposes a latency histogram, blocks indexed and head lag in Prometheus format.

## The contract

I planned `FraudRegistry.flag(address, score, bitmask)` and expected the workflow to call it. The CRE docs corrected me within an hour. A workflow does not call your contract. The DON signs a report, delivers it to a Chainlink `KeystoneForwarder`, and the forwarder calls `onReport(bytes metadata, bytes report)` on your contract after verifying the signatures. Your contract inherits `ReceiverTemplate`, implements `_processReport(bytes)`, and decodes the payload itself.

So `FraudRegistry` is forty lines on top of the template: a `FlagReport` struct, a mapping, an event, `isFlagged` and `getFlag` views. `flag(FlagReport)` still exists, always reverts, and is there only because the CRE binding generator emits a `WriteReportFromFlagReport` helper when it sees a public function taking that struct. Foundry tests cover the forwarder-only access path, the always-revert, and the ERC165 interface check.

One trap worth knowing before you deploy: simulation and production use different forwarders. `cre workflow simulate` writes through a `MockKeystoneForwarder` that passes no workflow metadata, so any workflow-ID or author check you enable in the contract makes simulation revert. Deploy with the mock forwarder address for the demo, switch to the real one with `setForwarderAddress` for production.

## The CRE workflow

Eighty lines of Go, compiled to WebAssembly with `//go:build wasip1`, run by every node in the DON. The SDK surface moves fast and the docs site is JavaScript-rendered, so I read the plain-text bundle at `docs.chain.link/cre/go/llms-full.txt` and wrote nothing from memory.

The trigger comes from generated bindings: drop the ERC20 ABI in `contracts/evm/src/abi/`, run `cre generate-bindings evm`, and you get `LogTriggerTransferLog(chainSelector, confidence, filters)` whose handler receives an already decoded `From`, `To`, `Value` plus the raw log's block number and tx hash.

The HTTP call is the part worth reading:

```go
type ScoreResponse struct {
    Address             string `json:"address"             consensus_aggregation:"identical"`
    Score               int    `json:"score"               consensus_aggregation:"identical"`
    RuleBitmask         uint32 `json:"ruleBitmask"         consensus_aggregation:"identical"`
    IndexedThroughBlock uint64 `json:"indexedThroughBlock" consensus_aggregation:"ignore"`
}

promise := http.SendRequest(config, runtime, client,
    fetchScore(addr, block),
    cre.ConsensusAggregationFromTags[*ScoreResponse](),
)
```

`http.SendRequest` is a map-reduce over the DON. Every node runs `fetchScore` itself, with its own HTTP client, against my service. The SDK then collects the structs and applies the tags: `identical` fields must agree across a Byzantine quorum, `ignore` fields are dropped. `Await()` returns one agreed struct or an error. For a price you would tag `median`; for a computed score there is no meaningful middle between 80 and 0, so it is `identical` or nothing.

Both parties of a transfer are scored, so both promises are opened before either is awaited. Each `Await` is a consensus round, and two sequential rounds inside a five-second HTTP budget is a bad idea.

The write is one call on the generated binding, `registry.WriteReportFromFlagReport(runtime, FlagReport{…}, gasConfig)`, which ABI-encodes the struct, asks the runtime for a DON-signed report, and hands it to the EVM write capability. The response carries the transaction hash.

Unit tests use the SDK's test runtime with stubbed HTTP and EVM capabilities: threshold gating, the exact 96-byte ABI payload, zero-address skipping on mints, self-transfers scored once, and a service error aborting before any write.

## Running it

```bash
docker compose up -d
cd service && go run ./cmd/indexer      # fills Postgres, keeps following the head
cd service && go run ./cmd/api          # :8080
curl "localhost:8080/score?address=0x0330070fd38ec3bb94f58fa55d40368271e9e54a"
cd contracts && forge script script/Deploy.s.sol --rpc-url sepolia --broadcast
cd workflow && cre workflow simulate fraud-flagger --target staging-settings \
    --evm-tx-hash 0x<a Transfer tx> --evm-event-index 0
cre workflow simulate fraud-flagger --target staging-settings --broadcast   # real tx
```

<!-- TODO: paste simulate output (dry-run and --broadcast) and the Etherscan link to the Flagged event once run. Do not publish before this is filled. -->

## What I hit

- **`forge` was Atlassian's.** `which forge` returned a Node binary from nvm. Foundry lives in `~/.foundry/bin` and must come first on `PATH`.
- **CRE needs Go 1.25.3.** My machine had 1.22. I installed 1.25.3 side by side rather than replacing it; the service still builds on 1.22.
- **The CLI installs to `~/.cre/bin/cre`**, not `~/.cre/cre` as I had noted from an older guide. Gatekeeper wants `xattr -c` on it.
- **`cre generate-bindings` works logged out, `cre init` and `simulate` do not.** I scaffolded the workflow directory by hand to the documented layout so the code could be written and unit-tested while waiting for the account.
- **The write model is not "call my function".** See the contract section; this changed the contract's shape and the kickoff spec.
- **Registering the same mock capability twice in one Go test fails** with `capability already exists`. One test runtime per test.

## What this proves and what it does not

The simulator is a single node. Consensus runs structurally, a report is produced and signed, but there is no Byzantine quorum because there is only one voter. The end-to-end demo proves the plumbing: trigger, fetch, aggregate, sign, forward, store. It does not prove the security model. That needs `cre workflow deploy` to a real DON, which needs deploy access from Chainlink and a service reachable from the public internet, neither of which a weekend has.

More fundamentally: the DON verifies that my service is **consistent**, not that it is **correct**. If the service returns the same wrong score to every node, the DON signs it. This is exactly the Synthetix failure in a different coat. The fix is the same as it was then: more than one independent source and an aggregation that tolerates one being wrong. With one scoring service, this oracle is more trustworthy than a bare API by precisely one property, tamper-evidence of the publication, and no more.

## Not built

- Deployment to a DON; demo is `simulate --broadcast` only.
- Multiple scoring sources with median or quorum across them.
- Reorg handling beyond "index finalized blocks only".
- Live sanctions refresh; the list is regenerated by hand.
- Any rule beyond the two above; no clustering, no exchange heuristics, no ML.
- Waiting for the indexer to reach the trigger block before scoring; the service clamps to what it has and reports the height it used.
- Mainnet, any other chain, a dashboard, alerts.
