# Building an On-chain Fraud Oracle with Go and Chainlink CRE

## Part 1: Design and Setup

*Weekend build, public repo, honest notes. Part 1 covers the problem, the architecture, what the Chainlink Runtime Environment (CRE) can and cannot do, and the environment setup. Code lands in Part 2.*

## The problem

Every ERC20 transfer is public, but "is this address suspicious?" is not an on-chain fact. Fraud signals live in graph structure: funds hopping through fresh wallets, proximity to sanctioned addresses, timing patterns. Computing that requires walking thousands of historical transfers, which no smart contract can afford to do.

So the analysis has to happen off-chain. The moment it does, you have a single server saying "trust me". A DeFi protocol cannot gate withdrawals on one company's HTTP endpoint.

This project closes that gap: a Go service scores addresses off-chain, and a Chainlink CRE workflow turns that score into an on-chain fact only after a decentralized oracle network (DON) has independently called the service and reached consensus on the answer.

## Architecture

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
                                 │ onReport → flag(addr,score)  │
                                 │ isFlagged(addr) view         │
                                 └──────────────────────────────┘
```

**Why analysis off-chain, publication via CRE.** Graph traversal over 50,000 blocks of transfers belongs in a process with a database and goroutines, not in a WASM sandbox with a 10 second HTTP budget. But publication is where trust matters. CRE runs the workflow on every DON node; each node calls `/score` itself, and only a value the nodes agree on gets signed and written. My API becomes one input to a verifiable oracle instead of the oracle itself.

## Scope for the weekend

Exactly this, nothing more:

- **Indexer.** ERC20 `Transfer` events for one token on Sepolia, last 50K blocks, concurrent range fetchers, Postgres checkpoint so a restart resumes instead of rescanning. Idempotent inserts keyed on `(tx_hash, log_index)`.
- **Two rules**, each a Go type behind one interface, unit tested on synthetic graphs:
  - *Peel chain*: funds move through a chain of fresh addresses, each hop forwarding most of the balance.
  - *Sanctions proximity*: address within 3 hops of a static sanctions list loaded from JSON.
- **API.** `GET /score?address=0x…&block=N` returning `{address, score 0-100, ruleBitmask, rules: [{name, evidence: [txHashes]}], indexedThroughBlock}`. Deterministic for the same address and block height, because the DON nodes will call it in parallel and compare answers. Prometheus `/metrics`.
- **Contract.** `FraudRegistry.sol`, minimal, deployed with Foundry.
- **Workflow.** CRE log trigger on `Transfer`, HTTP call with consensus, EVM write. Must run end to end in `cre workflow simulate` before anything is deployed.

Not built: more rules, a dashboard, mainnet, real-time OFAC fetching, reorg handling beyond "index finalized blocks only".

## What I verified in the CRE docs before writing a line

The CRE SDK surface moves fast, so I refused to write workflow code from memory. The docs site is JavaScript-rendered and useless to `curl`, but there is a plain text bundle at `https://docs.chain.link/cre/go/llms-full.txt`. Findings that shaped the design:

**1. A workflow cannot call an arbitrary contract function.** I had planned `FraudRegistry.flag(address, score, bitmask)` called directly from the workflow. That is not how EVM write works. The DON signs a report, sends it to a Chainlink `KeystoneForwarder` contract, and the forwarder calls `onReport(bytes metadata, bytes report)` on your contract. Your contract inherits `ReceiverTemplate`, implements `_processReport(bytes)`, and decodes the payload itself. The binding generator emits a `WriteReportFrom<Struct>` helper if the ABI has a public function taking that struct, so `flag` survives as a struct-typed function that mostly exists to guide the code generator.

**2. Simulation and production use different forwarders.** `cre workflow simulate` writes through a `MockKeystoneForwarder` (Sepolia: `0x15fC6ae953E024d975e77382eEeC56A9101f9F88`) that passes no workflow metadata. Enable workflow ID or author checks in the contract and simulation reverts. Production Sepolia forwarder is `0xF8344CFd5c43616a4366C34E3EEE75af79a74482`.

**3. Log triggers are generated from the ABI.** Drop `ERC20.abi` into `contracts/evm/src/abi/`, run `cre generate-bindings evm`, and you get `LogTriggerTransferLog(chainSelector, confidence, filters)`. The handler receives an already decoded `*bindings.DecodedLog[TransferDecoded]` with `Data.From`, `Data.To`, `Data.Value`, plus `Log.BlockNumber` and `Log.TxHash`. Sepolia is supported for both log triggers and writes; chain name `ethereum-testnet-sepolia`.

**4. HTTP consensus is a map-reduce.** `http.SendRequest(config, runtime, client, fetchFn, aggregation)` runs `fetchFn` on every node and reduces with an aggregation you choose. For a struct, tag fields: `consensus_aggregation:"identical"` for score and bitmask, `"ignore"` for the indexer height. Default request timeout is 5 seconds, maximum 10, and redirects fail. This forced a design decision: the workflow passes the trigger log's block number as `?block=N`, and the service scores at exactly that height, so nodes with slightly different indexer lag still return identical answers.

**5. The simulator is single-node.** Consensus runs structurally, not with Byzantine fault tolerance. Honest framing for the README: the end to end demo proves the plumbing, not the security model. Also, a deployed DON cannot reach `localhost`, so the demo lives in `cre workflow simulate`.

## Environment setup

What the audit found on my Mac, and what it took:

| Tool | Found | Needed |
|---|---|---|
| Go | 1.22.5 | 1.25.3+ (CRE Go SDK requirement) |
| Docker + Compose | 24.0.6 / v2.22 | fine |
| `forge` | Atlassian Forge CLI via nvm, not Foundry | Foundry |
| `cre` | missing | v1.35.0 |
| Postgres | client only | server via Docker |

The `forge` collision is worth calling out. `which forge` returned a Node binary under `~/.nvm`, and `forge --version` printed a warning about Node LTS releases. Foundry installs to `~/.foundry/bin`, which must come before the nvm paths.

```bash
brew install go
curl -L https://foundry.paradigm.xyz | bash
~/.foundry/bin/foundryup
curl -sSL https://app.chain.link/cre/install.sh | bash
xattr -c $HOME/.cre/bin/cre      # macOS Gatekeeper
echo 'export PATH="$HOME/.foundry/bin:$HOME/.cre/bin:$PATH"' >> ~/.zshrc
cre version                       # note: not --version
cre login                         # required even for local simulation
```

## Repo layout

```
fraud-oracle/
├── service/     Go module: indexer, rules, /score API
├── contracts/   Foundry: FraudRegistry.sol + ReceiverTemplate, deploy script
├── workflow/    CRE project (own go.mod, compiled to wasip1)
├── docker-compose.yml
├── .env.example
└── NOTES.md     what broke, what I worked around
```

Three Go modules on purpose. The CRE CLI wants its project root to be the module and compiles it for `wasip1`; the service has no business depending on `cre-sdk-go`.

## Postgres schema

```sql
CREATE TABLE transfers (
  block_number BIGINT        NOT NULL,
  tx_hash      BYTEA         NOT NULL,
  log_index    INTEGER       NOT NULL,
  from_addr    BYTEA         NOT NULL,
  to_addr      BYTEA         NOT NULL,
  value        NUMERIC(78,0) NOT NULL,   -- uint256, exact
  PRIMARY KEY (tx_hash, log_index)       -- insert ... ON CONFLICT DO NOTHING
);

CREATE TABLE checkpoint (
  id              SMALLINT PRIMARY KEY CHECK (id = 1),
  token           BYTEA  NOT NULL,
  chain_id        BIGINT NOT NULL,
  start_block     BIGINT NOT NULL,
  indexed_through BIGINT NOT NULL        -- contiguous high-water mark
);

CREATE TABLE range_done (                -- finished sub-ranges, workers complete out of order
  from_block BIGINT PRIMARY KEY,
  to_block   BIGINT NOT NULL
);
```

Workers pull 2,000 block ranges concurrently. Each range commits its transfers and a `range_done` row in one transaction. A coordinator advances `indexed_through` to the highest contiguous `to_block`. On restart the indexer resumes from `indexed_through + 1` and re-runs any range without a `range_done` row. If the checkpoint's token or chain differ from config, it refuses to start rather than silently rescanning.

## Next

Part 2: the indexer with checkpoint and resume, tested against a fake RPC and a real Postgres. Then rules, API, contract, and the first `cre workflow simulate` run. Everything in the repo, including the parts that did not work.

*Repo: github.com/<user>/fraud-oracle*
