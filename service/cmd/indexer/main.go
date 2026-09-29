// Command indexer backfills ERC20 Transfer logs into Postgres and follows the chain head.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"fraud-oracle/service/internal/indexer"
	"fraud-oracle/service/internal/rpc"
	"fraud-oracle/service/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rpcURL := mustEnv("SEPOLIA_RPC_URL")
	token := common.HexToAddress(mustEnv("TOKEN_ADDRESS"))
	dsn := mustEnv("DATABASE_URL")
	back := envUint("START_BLOCKS_BACK", 50_000)
	workers := int(envUint("WORKERS", 8))
	metricsAddr := envStr("INDEXER_METRICS_LISTEN", ":9090")

	st, err := store.Open(ctx, dsn)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	src, err := rpc.Dial(ctx, rpcURL, token)
	if err != nil {
		log.Error("rpc", "err", err)
		os.Exit(1)
	}
	defer src.Close()

	reg := prometheus.NewRegistry()
	m := indexer.NewMetrics(reg)
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		if err := http.ListenAndServe(metricsAddr, mux); err != nil {
			log.Error("metrics server", "err", err)
		}
	}()

	ix := indexer.New(indexer.Config{
		Token: token, StartBlocksBack: back, Workers: workers, FollowHead: true,
	}, src, st, m, log)
	if err := ix.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("indexer", "err", err)
		os.Exit(1)
	}
	log.Info("indexer stopped")
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing env", "key", k)
		os.Exit(2)
	}
	return v
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envUint(k string, def uint64) uint64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		slog.Error("bad env", "key", k, "value", v)
		os.Exit(2)
	}
	return n
}
