# Building an On-chain Fraud Oracle with Go and Chainlink CRE

*A weekend project: off-chain fraud detection in Go, published on-chain through a decentralized oracle network (DON), a set of independently operated Chainlink nodes that fetch, agree and sign together. Code: github.com/<user>/fraud-oracle.*

## Why put this on-chain at all

A fraud score is an opinion. Chainalysis has one, TRM has one, my Go service has one. Each lives behind an HTTP endpoint, each is a single company's word, and each can change its answer between two calls. That is fine for a compliance dashboard. It is not fine for a smart contract.

A contract cannot call an API. It cannot retry, cannot ask a second opinion, cannot tell a stale answer from a fresh one. Whatever it reads from storage, it acts on: block the withdrawal, reject the deposit, freeze the position. If that storage was written by one server, the protocol has outsourced its security to that server's uptime, honesty, and operator's private key.

This is the oracle problem, and the crypto industry has already paid for the lesson several times. In 2019 one of the price sources behind Synthetix's own oracle reported the Korean won at about 1000x its value, the aggregate followed it, and a trading bot minted roughly a billion dollars of synthetic assets before the trades were reversed. In 2022, as LUNA collapsed, the Chainlink LUNA/USD feed stopped at its configured minimum price of about $0.10 while the market was far lower; Venus Protocol read that floor as a real price, never checked it against bounds of its own, and lost about $11 million. In 2025 Moonwell lost money repeatedly to a feed returning an absurd price for a wrapped token that the protocol accepted without a sanity check.

Three failures, one shape: a single off-chain fact crossed into a contract without anyone independently checking it on the way in.

An on-chain fraud flag should not be another instance of that shape. The value of putting the flag on-chain is not that it is on-chain. Storage is cheap. The value is that **the act of writing it can be made verifiable**: multiple independent parties fetch the same answer, agree, and sign, and the contract accepts only their joint signature. Then the flag is a fact other contracts can build on, not a rumour one server posted.

The analysis stays off-chain, because graph traversal over fifty thousand blocks of transfers has no business inside a smart contract. The publication goes through the Chainlink Runtime Environment (CRE), because a decentralized oracle network, a DON, a fixed set of Chainlink nodes run by separate companies that each fetch the same data and sign only what they agree on, turns my single API into something a contract can trust more than me.

## What it does

One ERC20 token (the standard interface for fungible tokens) on Sepolia, Ethereum's public test network where the coins are worthless and the rules are real. For every `Transfer`, score the sender and the receiver 0 to 100 with two rules, and if either scores at or above a threshold, record it in a `FraudRegistry` contract that any other contract can query with `isFlagged(address)`.

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
| 1 | on-chain | `Transfer(from, to, value)` log emitted, block finalized (old enough that the chain will not reorganize it away, about 13 minutes on Ethereum) |
| 2 | off-chain | Indexer stores the log in Postgres |
| 3 | off-chain | CRE log trigger starts the workflow on every DON node |
| 4 | off-chain | Each node calls `GET /score?address=X&block=N` on its own |
| 5 | off-chain | Service runs the rules, returns score and evidence |
| 6 | off-chain | Nodes compare answers; identical ones get a DON signature |
| 7 | on-chain | `KeystoneForwarder` checks the signatures, calls `onReport` |
| 8 | on-chain | `FraudRegistry` decodes the report, emits `Flagged` |

Gas, the fee every transaction pays for the compute it uses, applies only to steps 1, 7 and 8. Step 1 is the user's own transaction; 7 and 8 are one transaction paid by the workflow owner's key. Step 6 is where one server's opinion becomes a verifiable fact.

## The off-chain service

### Indexer

An Ethereum node (the chain client, not an oracle node) answers "which logs did this contract emit in these blocks". They do not answer "who did this address send money to". For the second question you need your own table with an index on the address columns, and the indexer's whole job is to build it.

It pulls `Transfer` logs with `eth_getLogs` in 2,000-block ranges, eight ranges in flight, and writes them to Postgres keyed on `(tx_hash, log_index)` so a repeated insert is a no-op. Each range commits its rows together with a `range_done` marker in one transaction. A coordinator advances a single-row `checkpoint` only over contiguous completed ranges, so if range 5 finishes before range 3, the checkpoint waits at 2. Kill it, restart it, and it resumes from the checkpoint without re-fetching anything. The test for this uses a fake RPC that fails after two ranges, then confirms the second run makes exactly four more calls, not six.

Two choices came from running my own Ethereum and BSC nodes: index only up to `head - 64` so reorgs never reach the table, and split a range in half when the provider says the result is too large, because every provider has a different limit and none of them document it in the error.

### Rules

Each rule is one Go type behind one interface, evaluated against an in-memory graph built per request from the address's two-hop neighbourhood. The graph is sorted by `(block, log_index)` before any rule touches it, so the same database state always produces the same answer. That determinism is not tidiness; it is a requirement, and the reason is in the next section.

**Peel chain.** Funds move A → B → C → D, each receiver fresh (no activity before the hop), each forwarding at least 90% of what it received within 1,000 blocks. Three hops score 30, each extra hop adds 20, capped at 100. A round trip back to an earlier address is not a peel and terminates the chain. The term comes from UTXO-chain forensics, where each hop peels a small amount off; on an account-based chain the pattern is rarer and this rule is a first approximation. Its limit is plain: it follows the largest outgoing transfer greedily and misses a chain that splits.

**Sanctions proximity.** Breadth-first search, undirected, up to three hops from the address, against the 124 EVM-format addresses on the OFAC SDN list, the US Treasury's sanctions roster. The address itself on the list scores 100; one hop away 80, two 50, three 25. Evidence is the transfer path to the nearest listed address.

Undirected proximity has a known abuse: anyone can send dust from a listed address to a victim and put them one hop away. That happened at scale after the Tornado Cash designation in 2022. A production rule weights inbound taint far below outbound; this one does not, and the registry has no revocation, so a false positive is permanent. Both are listed under "Not built" because they are the next thing to build, not because they are optional.

The list is the real one. `cmd/ofac` downloads `sdn.xml` from treasury.gov, extracts every 0x address under a `Digital Currency Address` tag, and writes them to a JSON file the service loads at startup. The tag is per asset, not per chain: 120 entries say `ETH`, four more say `USDT`, `USDC`, `ARB`, `BSC` or `ETC` and are the same address format on the same or a compatible chain. My first version filtered on `ETH` alone and silently lost those four; a reviewer caught it. What is on it: Lazarus Group wallets, Garantex, SUEX, a dozen addresses belonging to an Iranian ransomware operator, fentanyl precursor suppliers. What is not on it: the Tornado Cash contracts, delisted in March 2025, which my first hand-written fixture still had.

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

### A race in the design

The indexer and the CRE trigger are two independent consumers of the same `Transfer` log. The trigger fires when the block is finalized. The indexer writes the same block when its poll loop reaches `head - 64`, which on Sepolia is also roughly finality. "Roughly" is the problem: nothing orders them. When the workflow calls `/score?block=N`, the indexer may not have reached N yet.

The service does not fail in that case. It clamps: `atBlock = min(N, indexed_through)`, scores at the height it actually has, and reports that height in `indexedThroughBlock`. Consensus still works, because every node gets the same clamped answer and the height field is excluded from comparison.

What is lost is the triggering transfer itself. If that transfer is the one that connects the address to a sanctioned wallet, the score at `indexed_through` does not see it yet. The address gets flagged on its next transfer, not this one. A delay of one transfer, deterministic, never a wrong answer, but a delay. For a blocklist that is acceptable. For a "reject this exact deposit" use case it is not, and the honest fix is in the service: when `block=N` arrives ahead of the checkpoint, wait for the indexer to catch up, bounded by the four-second budget, and only then score. I left that out of the weekend scope; it is a polling loop on the checkpoint, not a redesign.

The other direction, indexer ahead of the trigger, is harmless. Extra history below N is exactly what the graph wants.

## The contract

I planned `FraudRegistry.flag(address, score, bitmask)` and expected the workflow to call it. A workflow does not call your contract. The DON nodes each sign a report, the EVM write capability delivers it to a Chainlink `KeystoneForwarder`, and the forwarder calls `onReport(bytes metadata, bytes report)` on your contract after checking that enough of the DON's registered signers are on it. Your contract inherits `ReceiverTemplate`, implements `_processReport(bytes)`, and decodes the payload itself.

So `FraudRegistry` is forty lines on top of the template: a `FlagReport` struct, a mapping, an event, `isFlagged` and `getFlag` views. `flag(FlagReport)` still exists, always reverts, and is there only because the CRE binding generator emits a `WriteReportFromFlagReport` helper when it sees a public function taking that struct. Foundry tests cover the forwarder-only access path, the always-revert, and the ERC165 interface check.

One trap worth knowing before you deploy: simulation and production use different forwarders. `cre workflow simulate` writes through a `MockKeystoneForwarder` that passes no workflow metadata, so any workflow-ID or author check you enable in the contract makes simulation revert. Deploy with the mock forwarder address for the demo, switch to the real one with `setForwarderAddress` for production.

## The CRE workflow

Eighty lines of Go, compiled to WebAssembly with `//go:build wasip1`, run by every node in the DON. The SDK surface moves fast; the plain-text docs bundle at `docs.chain.link/cre/go/llms-full.txt` is the reliable reference.

The trigger comes from generated bindings: drop the ERC20 ABI in `contracts/evm/src/abi/`, run `cre generate-bindings evm`, and you get `LogTriggerTransferLog(chainSelector, confidence, filters)`, called here with `CONFIDENCE_LEVEL_FINALIZED`, whose handler receives an already decoded `From`, `To`, `Value` plus the raw log's block number and tx hash.

The HTTP call is the part worth reading:

```go
type ScoreResponse struct {
    Address             string `json:"address"             consensus_aggregation:"identical"`
    Score               int    `json:"score"               consensus_aggregation:"identical"`
    RuleBitmask         uint32 `json:"ruleBitmask"         consensus_aggregation:"identical"`
    IndexedThroughBlock uint64 `json:"indexedThroughBlock" consensus_aggregation:"ignore"`
}

// one promise per party, all opened before any Await: each Await is a consensus round
for i, a := range parties {
    promises[i] = http.SendRequest(config, runtime, client,
        fetchScore(a, block),
        cre.ConsensusAggregationFromTags[*ScoreResponse](),
    )
}
for i := range parties {
    s, err := promises[i].Await()
    ...
}
```

`http.SendRequest` is a map-reduce over the DON. Every node runs `fetchScore` itself, with its own HTTP client, against my service. The SDK then collects the structs and applies the tags: `identical` fields must agree across a Byzantine quorum (with n nodes tolerating f faulty ones, n ≥ 3f+1, at least 2f+1 must match), `ignore` fields are dropped. `Await()` returns one agreed struct or an error. For a price you would tag `median`; for a computed score there is no meaningful middle between 80 and 0, so it is `identical` or nothing.

The write is one call on the generated binding, `registry.WriteReportFromFlagReport(runtime, FlagReport{…}, gasConfig)`, which ABI-encodes the struct, asks the runtime for a DON-signed report, and hands it to the EVM write capability. The response carries the transaction hash.

Unit tests use the SDK's test runtime with stubbed HTTP and EVM capabilities: threshold gating, the exact 96-byte ABI payload, zero-address skipping on mints, self-transfers scored once, and a service error aborting before any write.

## Running it

Three processes and one contract. Each step is runnable on its own, and each one tells you something if it fails.

**1. Postgres and the indexer.** The indexer needs `SEPOLIA_RPC_URL`, `TOKEN_ADDRESS` and `DATABASE_URL` in `.env`. First run creates the checkpoint at `head - 64 - 50000` and backfills; on a public RPC that is a few minutes, on your own node under a minute. Kill it with Ctrl-C at any point and restart: the log line `checkpoint resumed indexedThrough=N` is the resume path working.

```bash
docker compose up -d --wait
set -a && source .env && set +a
cd service && go run ./cmd/indexer
```

Progress is on `:9090/metrics` as `indexer_blocks_indexed_total` and `indexer_head_lag_blocks`. Lag going to zero means backfill is done and it is following the head.

**2. The API.** Loads the sanctions list once, then serves. Until the indexer has a checkpoint, `/score` answers 503, which is the right answer: scoring an empty graph would return 0 for everything and look fine.

```bash
cd service && go run ./cmd/api
curl "localhost:8080/score?address=0x0330070fd38ec3bb94f58fa55d40368271e9e54a"
```

That address is on the OFAC list, so it scores 100 with no graph at all. Call it twice: the bodies are byte-identical, which is the property the DON depends on. Then pick any address from the indexed token's recent transfers and add `&block=` to see the clamp.

**3. The contract.** Deployed with the Sepolia `MockKeystoneForwarder` as constructor argument, because that is what the simulator writes through. `forge script` prints the address; it goes into `workflow/fraud-flagger/config.staging.json` as `registryAddress`.

```bash
cd contracts && forge script script/Deploy.s.sol --rpc-url sepolia --broadcast
```

**4. The workflow, dry run.** The simulator compiles the Go to WASM, fetches the log for the transaction hash you give it from your RPC, and runs the handler once. No gas: the EVM write is prepared and reported but not sent, so `txHash` comes back as `0x`. The CLI reads `workflow/.env` for `CRE_ETH_PRIVATE_KEY` (64 hex characters, no `0x`) and `SEPOLIA_RPC_URL` even for a dry run.

```bash
cd workflow && cre workflow simulate fraud-flagger --target staging-settings \
    --evm-tx-hash 0x<a Transfer tx of the token> --evm-event-index 0
```

Pick a transaction whose sender or receiver scores at or above the threshold, or the handler legitimately does nothing and the output ends at two `scored` lines. For the demo I used a transfer touching a listed address.

**5. The workflow, for real.** Same command with `--broadcast`. The simulator sends the report to the mock forwarder, which calls `onReport` on the registry, which emits `Flagged`. The log line `flag written` carries the transaction hash; `cast call <registry> "isFlagged(address)(bool)" <subject> --rpc-url sepolia` returns `true` afterwards.

```bash
cre workflow simulate fraud-flagger --target staging-settings --broadcast \
    --evm-tx-hash 0x<same tx> --evm-event-index 0
```

<!-- TODO: paste trimmed simulate output for step 4 (scored lines + Workflow Simulation Result JSON) and step 5 (flag written line), and the Etherscan link to the Flagged event. Do not publish before this is filled. -->

## What this proves and what it does not

The simulator is a single node. Consensus runs structurally, a report is produced and signed, but there is no Byzantine quorum because there is only one voter. The end-to-end demo proves the plumbing: trigger, fetch, aggregate, sign, forward, store. It does not prove the security model. That needs `cre workflow deploy` to a real DON, which needs deploy access from Chainlink and a service reachable from the public internet, neither of which a weekend has.

More fundamentally: the DON verifies that my service is **consistent**, not that it is **correct**. If the service returns the same wrong score to every node, the DON signs it. Synthetix aggregated bad data; this design has one source and no aggregation at all, which is worse. The fix is the one the feeds use: several independent sources and a median across them. With one scoring service, this oracle is more trustworthy than a bare API by precisely one property, tamper-evidence of the publication, and no more.

## CRE is new, and that cuts both ways

Price feeds ran for years without node software executing customer code. Chainlink Functions started that in 2023 with JavaScript in a sandbox; CRE goes further, running customer WebAssembly as the primary product, since late 2025. The surface is new enough that the questions below are what I would open a security review with.

**Correlated failure.** Byzantine fault tolerance assumes nodes fail independently. A workflow runs on every node at the same moment, on the same WASM runtime (Wasmtime, per the docs), on the same node binary. A runtime escape would not take down one node, it would take down all of them in the same execution. The 2f+1 arithmetic does not help against a bug every voter shares. Wasmtime's Cranelift compiler has had several memory-safety CVEs, one of them a sandbox escape; it is well audited, and "well audited" is not "closed". Chainlink's answer, as far as the public docs show it, is procedural: `cre workflow deploy` needs account approval, the WASM hash is pinned in an on-chain registry, and per-workflow quotas cap memory and execution time. That stops a stranger and stops a runaway loop. It does not stop an approved customer with an unknown escape. The mitigation I would want to see is runtime diversity, two engines where a bug in one is outvoted by the other, and that is not on any roadmap I can find.

**Capability reach.** The sandbox has no network of its own, but the HTTP capability gives a workflow the node's network. The docs forbid redirects and cap response size and timeout. They do not say whether a workflow can address `169.254.169.254` or an operator's internal services. If it can, a workflow is a server-side request forgery primitive running on every node operator's infrastructure at once. Operators presumably sit behind an egress proxy; I could not confirm it from the documentation.

**Shared node, many tenants.** Each execution gets a fresh WASM instance and a fresh linear memory, and executions are stateless, so cross-workflow leakage through memory needs the same runtime escape as above. Secrets are scoped to the workflow owner through a Vault DON. Side channels between executions on one host are theoretically possible and, to my knowledge, unstudied for this setup.

**Determinism as a denial-of-service lever.** Consensus requires identical output from every node. A workflow that reads the clock or iterates a Go map in output order never agrees with itself and never writes. It also means a malicious or buggy data source can stall a workflow forever by returning slightly different bodies to different nodes. My service avoids that by pinning `block=N`; a service that does not is a workflow that never fires, with no error anyone sees.

None of this is a reason to avoid CRE. It is a year-old execution environment with a strong design and a short track record, and those are the questions to ask it.

## Not built

- Deployment to a DON; demo is `simulate --broadcast` only.
- Multiple scoring sources with median or quorum across them.
- Reorg handling beyond "index finalized blocks only".
- Live sanctions refresh; the list is regenerated by hand. Chainalysis also publishes a free on-chain `isSanctioned(address)` oracle on mainnet; it answers for one address, not a neighbourhood, so it was not a fit here.
- Inbound/outbound taint weighting, flag expiry or revocation, exchange heuristics; no third rule.
- Waiting for the indexer to reach the trigger block before scoring; see "A race in the design".
- Mainnet, any other chain, a dashboard, alerts.
