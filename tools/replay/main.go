// TRON Mainnet → Private Chain Transaction Replayer.
//
// Flow:
//  1. Fetch mainnet blocks via the TronGrid HTTP API.
//  2. Extract transactions from each block.
//  3. POST each transaction to the private node's
//     /wallet/broadcasttransaction endpoint.
//  4. Write failed / skipped txs to JSONL logs; advance state.json after
//     each successful block.
//
// The private chain node must run a relay_skip_signature-style branch
// that disables refBlockHash / expiration / TaPos and selected signature
// checks. See the README "Required java-tron patches" section.
//
// Usage: see README.md in this directory.
//
// Dependencies: Go standard library + tools/common/broadcast.
// Go version: 1.21+
//
// File layout:
//   - main.go      entry point, CLI flags, Config
//   - state.go     state file read/write
//   - trongrid.go  TronGrid API client
//   - filter.go    transaction filter rules
//   - logger.go    JSONL logger
//   - replayer.go  main loop, pacing control
//
// HTTP broadcast lives in tools/common/broadcast (shared with txgen).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/tronprotocol/tron-deployment/tools/common/broadcast"
)

// Config is the runtime configuration parsed from CLI flags + env vars.
type Config struct {
	TrongridURL   string
	TrongridKey   string
	TrongridQPS   int
	TpsMultiplier float64 // private TPS = mainnet TPS * multiplier; pace_sec = 3 / multiplier
	PrivateNode   string
	Start         int64
	End           int64
	StateFile     string
	FailLog       string
	SkipLog       string
	IncludeAll    bool
}

func parseArgs() Config {
	var c Config
	flag.StringVar(&c.TrongridURL, "trongrid-url", trongridDefaultURL, "TronGrid API base URL")
	flag.StringVar(&c.TrongridKey, "trongrid-key", os.Getenv("TRONGRID_API_KEY"),
		"TronGrid API key (or env TRONGRID_API_KEY)")
	flag.IntVar(&c.TrongridQPS, "trongrid-qps", trongridDefaultQPS,
		"TronGrid request rate limit (queries per second)")
	flag.Float64Var(&c.TpsMultiplier, "tps-multiplier", tpsMultiplierDefault,
		"Private chain TPS as a fraction of mainnet TPS. "+
			"pace_sec = 3 / multiplier. "+
			"Examples: 1 → 3s/block (mainnet speed), 0.5 → 6s/block, "+
			"default ~0.333 → 9s/block (3 SR private vs 27 SR mainnet)")
	flag.StringVar(&c.PrivateNode, "private-node", "",
		"Private chain HTTP API base, e.g. http://10.0.0.1:8090 (required)")
	flag.Int64Var(&c.Start, "start", 0,
		"Start MAINNET block (inclusive). 0 = resume from state file "+
			"(required on first run; do NOT use private chain head)")
	flag.Int64Var(&c.End, "end", 0,
		"End block (inclusive). 0 = start + 10 (short range for safety; "+
			"set explicitly for longer runs)")
	flag.StringVar(&c.StateFile, "state-file", "./replay-state.json", "Resume state file")
	flag.StringVar(&c.FailLog, "fail-log", "./replay-failures.jsonl", "Failed broadcast log")
	flag.StringVar(&c.SkipLog, "skip-log", "./replay-skips.jsonl", "Skipped txs log")
	flag.BoolVar(&c.IncludeAll, "include-all", false,
		"Do not skip Vote/Witness/Withdraw txs (default skips them, see 0.5.3)")
	flag.Parse()
	if c.PrivateNode == "" {
		fmt.Fprintln(os.Stderr, "error: --private-node is required")
		flag.Usage()
		os.Exit(2)
	}
	return c
}

// keysOf returns all keys of m, used only for startup logging.
func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func main() {
	if err := run(); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}

// run is the real entry point. It returns an error instead of calling
// log.Fatalf so deferred cleanups (signal.NotifyContext restore, jsonl
// loggers close, etc.) actually run before the process exits.
func run() error {
	cfg := parseArgs()

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	skipTypes := map[string]struct{}{}
	if !cfg.IncludeAll {
		skipTypes = defaultSkipContractTypes
	}

	failLog, err := openJsonlLogger(cfg.FailLog)
	if err != nil {
		return fmt.Errorf("open fail log: %w", err)
	}
	defer failLog.Close()
	skipLog, err := openJsonlLogger(cfg.SkipLog)
	if err != nil {
		return fmt.Errorf("open skip log: %w", err)
	}
	defer skipLog.Close()

	r := &Replayer{
		cfg:       cfg,
		trongrid:  newTronGridClient(cfg.TrongridURL, cfg.TrongridKey, cfg.TrongridQPS),
		private:   broadcast.New(cfg.PrivateNode),
		state:     loadState(cfg.StateFile),
		skipTypes: skipTypes,
		failLog:   failLog,
		skipLog:   skipLog,
	}

	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}
