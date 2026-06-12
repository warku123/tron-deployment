package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Config is txgen's runtime configuration, loaded from a JSON file.
//
// All paths in the file are resolved relative to the working
// directory of the running binary, not the config file's location.
type Config struct {
	// Node — the TRON HTTP endpoint used for createtransaction calls
	// during generate, and for broadcast / block lookups otherwise.
	Node string `json:"node"`

	// Generate: build signed txs and write them to CSV.
	Generate struct {
		TotalTxCount         int    `json:"totalTxCount"`
		ReceiverAddressCount int    `json:"receiverAddressCount"`
		Concurrency          int    `json:"concurrency"`
		PrivateKey           string `json:"privateKey"`
		OutputDir            string `json:"outputDir"`
		TxType               struct {
			Transfer      int `json:"transfer"`
			TransferTRC10 int `json:"transferTrc10"`
			TransferTRC20 int `json:"transferTrc20"`
		} `json:"txType"`
		TRC10ID        string `json:"trc10Id"`
		TRC20Address   string `json:"trc20Address"`
		TransferAmount int64  `json:"transferAmount"`
		TRC20FeeLimit  int64  `json:"trc20FeeLimit"`

		// PQ: when enabled, a `ratio` percent of transactions are signed
		// with a post-quantum scheme and carry a pq_auth_sig instead of the
		// ECDSA signature; the remainder are ECDSA-signed. The PQ sender is
		// derived from the PQ seed (its account permission must already
		// register this PQ public key); the ECDSA sender from privateKey.
		PQ struct {
			Enabled bool   `json:"enabled"`
			Scheme  string `json:"scheme"` // only "ML_DSA_44" is supported
			Seed    string `json:"seed"`   // 32-byte hex (64 hex chars)
			Ratio   int    `json:"ratio"`  // percent of txs PQ-signed, 1-100 (default 100)
		} `json:"pq"`
	} `json:"generate"`

	// Broadcast: read CSV, fire to node at tpsLimit, write txIDs.
	Broadcast struct {
		InputDir   string `json:"inputDir"`
		TpsLimit   int    `json:"tpsLimit"`
		SaveTxID   bool   `json:"saveTxId"`
		TxIDFile   string `json:"txIdFile"`
		ReportFile string `json:"reportFile"`
	} `json:"broadcast"`

	// Statistic: post-broadcast TPS calculation across a block range.
	Statistic struct {
		StartBlock int64  `json:"startBlock"`
		EndBlock   int64  `json:"endBlock"`
		OutputFile string `json:"outputFile"`
	} `json:"statistic"`
}

// LoadConfig reads + validates a JSON config file. Defaults are filled
// in for unset numeric fields so a minimal config is still usable.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Node == "" {
		c.Node = "http://127.0.0.1:8090"
	}
	if c.Generate.Concurrency == 0 {
		c.Generate.Concurrency = 8
	}
	if c.Generate.OutputDir == "" {
		c.Generate.OutputDir = "txgen-output"
	}
	if c.Generate.ReceiverAddressCount == 0 {
		c.Generate.ReceiverAddressCount = 1000
	}
	if c.Generate.TransferAmount == 0 {
		c.Generate.TransferAmount = 1
	}
	if c.Generate.TRC20FeeLimit == 0 {
		c.Generate.TRC20FeeLimit = 100_000_000 // 100 TRX
	}
	if c.Generate.PQ.Enabled {
		if c.Generate.PQ.Scheme == "" {
			c.Generate.PQ.Scheme = SchemeMLDSA44
		}
		// An omitted ratio defaults to 100% PQ; to disable PQ entirely set
		// pq.enabled = false rather than ratio = 0.
		if c.Generate.PQ.Ratio == 0 {
			c.Generate.PQ.Ratio = 100
		}
	}
	if c.Broadcast.InputDir == "" {
		c.Broadcast.InputDir = c.Generate.OutputDir
	}
	if c.Broadcast.TpsLimit == 0 {
		c.Broadcast.TpsLimit = 1000
	}
	if c.Broadcast.TxIDFile == "" {
		c.Broadcast.TxIDFile = "broadcast-txid.csv"
	}
	if c.Broadcast.ReportFile == "" {
		c.Broadcast.ReportFile = "broadcast-report.txt"
	}
	if c.Statistic.OutputFile == "" {
		c.Statistic.OutputFile = "tps-statistic.txt"
	}
}

// validate is only meaningful for the `generate` subcommand. Other
// subcommands ignore the `generate` section entirely, so we don't fail
// here for missing fields — runGenerate will surface them with a clear
// error if it actually needs them.
func (c *Config) validate() error {
	tt := c.Generate.TxType
	sum := tt.Transfer + tt.TransferTRC10 + tt.TransferTRC20
	// Skip generate-section validation if all three weights are zero —
	// the user is running a different subcommand and didn't fill it in.
	if sum == 0 {
		return nil
	}
	if sum != 100 {
		return fmt.Errorf("generate.txType weights must sum to 100, got %d", sum)
	}
	if c.Generate.PQ.Enabled {
		if c.Generate.PQ.Scheme != SchemeMLDSA44 {
			return fmt.Errorf("generate.pq.scheme %q unsupported (only %s)", c.Generate.PQ.Scheme, SchemeMLDSA44)
		}
		if len(c.Generate.PQ.Seed) != pqSeedHexLen {
			return fmt.Errorf("generate.pq.seed must be %d hex chars (32 bytes)", pqSeedHexLen)
		}
		if c.Generate.PQ.Ratio < 1 || c.Generate.PQ.Ratio > 100 {
			return fmt.Errorf("generate.pq.ratio must be 1-100, got %d", c.Generate.PQ.Ratio)
		}
	}
	// The ECDSA privateKey is needed whenever some txs are ECDSA-signed:
	// PQ off entirely, or PQ on but signing less than 100% of txs.
	if !c.Generate.PQ.Enabled || c.Generate.PQ.Ratio < 100 {
		if c.Generate.PrivateKey == "" {
			return errors.New("generate.privateKey is required")
		}
		if len(c.Generate.PrivateKey) != PrivateKeyHexLen {
			return fmt.Errorf("generate.privateKey must be %d hex chars", PrivateKeyHexLen)
		}
	}
	if tt.TransferTRC10 > 0 && c.Generate.TRC10ID == "" {
		return errors.New("generate.trc10Id is required when transferTrc10 > 0")
	}
	if tt.TransferTRC20 > 0 && c.Generate.TRC20Address == "" {
		return errors.New("generate.trc20Address is required when transferTrc20 > 0")
	}
	if c.Generate.TotalTxCount <= 0 {
		return errors.New("generate.totalTxCount must be > 0")
	}
	if c.Generate.ReceiverAddressCount <= 0 {
		return errors.New("generate.receiverAddressCount must be > 0")
	}
	return nil
}
