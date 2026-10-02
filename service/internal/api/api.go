// Package api serves GET /score, /metrics and /healthz.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"fraud-oracle/service/internal/graph"
	"fraud-oracle/service/internal/rules"
	"fraud-oracle/service/internal/score"
	"fraud-oracle/service/internal/store"
)

// neighbourhoodRows caps how many transfers one expansion step loads.
// Above this, hot addresses (exchanges) would blow the CRE 5s HTTP budget.
const neighbourhoodRows = 5000

// ScoreResponse is the wire format. Field order and lowercase hex are fixed because
// CRE nodes compare answers byte for byte after JSON decode.
type ScoreResponse struct {
	Address             string             `json:"address"`
	Score               int                `json:"score"`
	RuleBitmask         uint32             `json:"ruleBitmask"`
	Rules               []score.RuleResult `json:"rules"`
	IndexedThroughBlock uint64             `json:"indexedThroughBlock"`
}

type Server struct {
	st      *store.Store
	rules   []rules.Rule
	log     *slog.Logger
	reg     *prometheus.Registry
	latency prometheus.Histogram
	router  chi.Router
}

func New(st *store.Store, rs []rules.Rule, log *slog.Logger) *Server {
	s := &Server{st: st, rules: rs, log: log, reg: prometheus.NewRegistry()}
	s.latency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "score_latency_seconds",
		Help:    "Latency of GET /score.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})
	s.reg.MustRegister(s.latency)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(4 * time.Second)) // stay under CRE's 5s default
	r.Get("/score", s.handleScore)
	r.Get("/healthz", s.handleHealth)
	r.Handle("/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	s.router = r
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.router.ServeHTTP(w, r) }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if _, err := s.st.GetCheckpoint(r.Context()); err != nil {
		http.Error(w, "no checkpoint", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleScore(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() { s.latency.Observe(time.Since(start).Seconds()) }()

	q := r.URL.Query().Get("address")
	if !common.IsHexAddress(q) {
		http.Error(w, `invalid or missing "address"`, http.StatusBadRequest)
		return
	}
	addr := common.HexToAddress(q)

	cp, err := s.st.GetCheckpoint(r.Context())
	if errors.Is(err, store.ErrNoCheckpoint) {
		http.Error(w, "indexer has not run yet", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	atBlock := cp.IndexedThrough
	if b := r.URL.Query().Get("block"); b != "" {
		n, err := strconv.ParseUint(b, 10, 64)
		if err != nil {
			http.Error(w, `invalid "block"`, http.StatusBadRequest)
			return
		}
		atBlock = min(n, cp.IndexedThrough)
	}

	resp, err := s.Score(r.Context(), addr, atBlock)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// graphDepth is how far the loader expands from the subject. Both rules look up to three
// transfers away (peel chain MinHops=3, sanctions MaxHops=3), so the graph must contain
// transfers three hops out or the rules cannot see what they are scoring.
const graphDepth = 3

// Score loads the subject's neighbourhood to graphDepth at or below atBlock and runs the rules.
// Same (addr, atBlock, DB contents) always gives the same response.
func (s *Server) Score(ctx context.Context, addr common.Address, atBlock uint64) (ScoreResponse, error) {
	var all []store.Transfer
	seen := map[common.Address]bool{addr: true}
	frontier := []common.Address{addr}
	for depth := 0; depth < graphDepth && len(frontier) > 0; depth++ {
		ts, err := s.st.TransfersTouching(ctx, frontier, atBlock, neighbourhoodRows)
		if err != nil {
			return ScoreResponse{}, err
		}
		all = append(all, ts...)
		var next []common.Address
		for _, t := range ts {
			for _, a := range [...]common.Address{t.From, t.To} {
				if !seen[a] {
					seen[a] = true
					next = append(next, a)
				}
			}
		}
		frontier = next
	}
	g := graph.New(all)
	res := score.Evaluate(g, addr, s.rules)
	if res.Rules == nil {
		res.Rules = []score.RuleResult{}
	}
	return ScoreResponse{
		Address:             addr.Hex(),
		Score:               res.Score,
		RuleBitmask:         res.RuleBitmask,
		Rules:               res.Rules,
		IndexedThroughBlock: atBlock,
	}, nil
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.log.Error("score", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
