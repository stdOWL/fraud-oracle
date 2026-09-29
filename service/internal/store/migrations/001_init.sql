CREATE TABLE IF NOT EXISTS transfers (
  block_number  BIGINT        NOT NULL,
  tx_hash       BYTEA         NOT NULL,
  log_index     INTEGER       NOT NULL,
  from_addr     BYTEA         NOT NULL,
  to_addr       BYTEA         NOT NULL,
  value         NUMERIC(78,0) NOT NULL,
  PRIMARY KEY (tx_hash, log_index)
);
CREATE INDEX IF NOT EXISTS transfers_from_block ON transfers (from_addr, block_number);
CREATE INDEX IF NOT EXISTS transfers_to_block   ON transfers (to_addr,   block_number);
CREATE INDEX IF NOT EXISTS transfers_block      ON transfers (block_number);

CREATE TABLE IF NOT EXISTS checkpoint (
  id              SMALLINT PRIMARY KEY CHECK (id = 1),
  token           BYTEA  NOT NULL,
  chain_id        BIGINT NOT NULL,
  start_block     BIGINT NOT NULL,
  indexed_through BIGINT NOT NULL,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS range_done (
  from_block BIGINT PRIMARY KEY,
  to_block   BIGINT NOT NULL
);
