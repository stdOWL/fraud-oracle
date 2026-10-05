# Flow charts

## System overview

```mermaid
flowchart TB
    T[Sepolia ERC20 token<br/>Transfer logs]
    S[service/ Go, offchain<br/>indexer · Postgres · rules · GET /score]
    W[workflow/ CRE, DON<br/>log trigger · HTTP consensus · EVM write]
    R[FraudRegistry.sol<br/>onReport via forwarder · isFlagged]
    T --> S
    T --> W
    W -- GET /score?address&block --> S
    W -- signed report --> R
```

## Per-transfer flow

```mermaid
flowchart TB
    A[Transfer log finalized<br/>EVM log trigger fires]
    B[Every DON node calls<br/>GET /score?address=X&block=logBlock]
    C[Consensus aggregation<br/>score and bitmask must be identical]
    D{score ≥ threshold?}
    E[WriteReportFromFlagReport<br/>KeystoneForwarder → onReport → Flagged event]
    F[Log and return]
    A --> B --> C --> D
    D -- yes --> E
    D -- no --> F
```

## Indexer flow

```mermaid
flowchart TB
    S0[Start: read checkpoint] --> S1{checkpoint exists?}
    S1 -- no --> S2[start_block = head - 50000<br/>insert checkpoint]
    S1 -- yes --> S3{token/chain match config?}
    S3 -- no --> X[refuse to start]
    S3 -- yes --> S4[resume from indexed_through + 1]
    S2 --> S5
    S4 --> S5[split into 2000-block ranges]
    S5 --> S6[N workers: eth_getLogs<br/>insert ON CONFLICT DO NOTHING<br/>+ range_done row, one tx]
    S6 --> S7[coordinator: advance indexed_through<br/>to highest contiguous range]
    S7 --> S8[follow head: every 12s index up to head-64]
```

## Sanctions proximity: 3-hop BFS

```mermaid
flowchart TB
    Q[GET /score?address=A&block=N<br/>called by every CRE node] --> L[Postgres: neighbourhood of A, up to 7 hops<br/>block_number ≤ N, ordered by block, logIndex]
    L --> G[In-memory graph: Out a, In a<br/>undirected edge = sent or received]
    G --> D{A on the list?}
    D -- yes --> S100[score 100, stop]
    D -- no --> B[BFS hop 1, 2, 3<br/>map lookup per neighbour, stop at first hit]
    B --> H1[hop 1 · ~20 addrs · score 80]
    B --> H2[hop 2 · ~400 addrs · score 50]
    B --> H3[hop 3 · ~8000 addrs · score 25]
    H1 --> F[Finding score, evidence<br/>evidence = tx hashes on the path]
    H2 --> F
    H3 --> F
    J[sanctions.json, 124 addrs<br/>OFAC sdn.xml loaded at startup] -.-> B
```

Why the list lives in the service and not behind an external API: one `/score` call can touch thousands of addresses across three hops. A per-address screening API at 30–100 requests/hour cannot serve that, and its answers could differ between CRE nodes. A static list pinned at commit time is both fast and deterministic.

## On-chain vs off-chain, step by step

| # | Where | Step | Who runs it |
|---|---|---|---|
| 1 | on-chain | ERC20 `Transfer(from,to,value)` log emitted, block finalized | Sepolia |
| 2 | off-chain | Indexer stores the log in Postgres, advances checkpoint | `service/cmd/indexer` |
| 3 | off-chain | CRE log trigger fires the workflow on every DON node | CRE DON |
| 4 | off-chain | Each node calls `GET /score?address=X&block=N` independently | CRE HTTP capability |
| 5 | off-chain | Rules run: peel chain + sanctions BFS, score 0-100 | `service/cmd/api` |
| 6 | off-chain | Consensus: identical score/bitmask required; DON signs a report | CRE consensus |
| 7 | on-chain | `KeystoneForwarder` verifies signatures, calls `onReport` | Chainlink forwarder |
| 8 | on-chain | `FraudRegistry` decodes `FlagReport`, emits `Flagged`, `isFlagged(addr)` = true | our contract |

Only steps 1, 7, 8 cost gas. Steps 2-6 are free compute; step 6 is the only one that turns a single server's opinion into a verifiable fact.

```mermaid
flowchart LR
    subgraph ON[on-chain · Sepolia]
        T[1. Transfer log] --> F7[7. KeystoneForwarder] --> R[8. FraudRegistry]
    end
    subgraph OFF[off-chain]
        I[2. Indexer] --> S[5. Rules / score]
        W3[3. CRE trigger] --> W4[4. HTTP /score] --> S --> W6[6. Consensus + sign]
    end
    T --> I
    T --> W3
    W6 --> F7
```
