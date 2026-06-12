package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

// TPSStat is the result of summing block tx counts over [start, end].
type TPSStat struct {
	StartBlock     int64
	EndBlock       int64
	StartTimestamp int64 // ms since epoch
	EndTimestamp   int64
	TotalTx        int
	MaxBlockSize   int
	MinBlockSize   int
	EmptyBlocks    int
	BlocksObserved int
}

// runStatistic loads cfg.Statistic.{StartBlock,EndBlock} and computes
// on-chain TPS across that range. Result is written to
// cfg.Statistic.OutputFile and also echoed to stdout.
func runStatistic(ctx context.Context, cfg *Config) error {
	if cfg.Statistic.StartBlock <= 0 || cfg.Statistic.EndBlock <= 0 {
		return fmt.Errorf("statistic.startBlock and statistic.endBlock are required")
	}
	if cfg.Statistic.EndBlock < cfg.Statistic.StartBlock {
		return fmt.Errorf("statistic.endBlock must be >= statistic.startBlock")
	}

	node := NewNodeClient(cfg.Node, 10*time.Second)
	stat, err := computeTPS(ctx, node, cfg.Statistic.StartBlock, cfg.Statistic.EndBlock)
	if err != nil {
		return err
	}

	report := formatStatReport(stat)
	if err := os.WriteFile(cfg.Statistic.OutputFile, []byte(report), 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	log.Printf("statistic: report → %s", cfg.Statistic.OutputFile)
	fmt.Println(report)
	return nil
}

// computeTPS walks blocks [start, end] inclusive on the node, summing
// transactions and recording timestamps for TPS math.
func computeTPS(ctx context.Context, node *NodeClient, start, end int64) (*TPSStat, error) {
	stat := &TPSStat{
		StartBlock:   start,
		EndBlock:     end,
		MinBlockSize: -1,
	}
	for n := start; n <= end; n++ {
		if ctx.Err() != nil {
			return stat, ctx.Err()
		}
		b, err := node.GetBlockByNum(ctx, n)
		if err != nil {
			return stat, fmt.Errorf("fetch block %d: %w", n, err)
		}
		if b == nil {
			stat.EmptyBlocks++
			continue
		}
		stat.BlocksObserved++
		sz := len(b.Transactions)
		stat.TotalTx += sz
		if sz > stat.MaxBlockSize {
			stat.MaxBlockSize = sz
		}
		if stat.MinBlockSize < 0 || sz < stat.MinBlockSize {
			stat.MinBlockSize = sz
		}
		if n == start {
			stat.StartTimestamp = b.BlockHeader.RawData.Timestamp
		}
		if n == end {
			stat.EndTimestamp = b.BlockHeader.RawData.Timestamp
		}
		if (n-start)%100 == 0 && n != start {
			log.Printf("statistic: scanned %d/%d blocks", n-start, end-start)
		}
	}
	if stat.MinBlockSize < 0 {
		stat.MinBlockSize = 0
	}
	return stat, nil
}

// formatStatReport is what `txgen statistic` prints.
func formatStatReport(s *TPSStat) string {
	span := s.EndTimestamp - s.StartTimestamp
	minutes := float64(span) / 1000.0 / 60.0
	var tps float64
	if span > 0 {
		tps = float64(s.TotalTx) * 1000.0 / float64(span)
	}
	missRate := 0.0
	if total := s.EndBlock - s.StartBlock + 1; total > 0 {
		missRate = float64(s.EmptyBlocks) / float64(total)
	}
	return fmt.Sprintf(`TPS statistic report:
block range:        startBlock: %d, endBlock: %d
blocks observed:    %d (empty: %d)
total tx count:     %d
max block size:     %d
min block size:     %d
cost time:          %.2f minutes
tps:                %.2f
miss block rate:    %.4f
`,
		s.StartBlock, s.EndBlock,
		s.BlocksObserved, s.EmptyBlocks,
		s.TotalTx, s.MaxBlockSize, s.MinBlockSize,
		minutes, tps, missRate,
	)
}

type reportInput struct {
	TpsLimit           int
	TotalGenerated     int
	TotalBroadcastOK   int
	TotalBroadcastFail int
	CostTime           time.Duration
	Stat               *TPSStat
}

// formatReport is what `txgen broadcast` prints. The shape mirrors the
// upstream tron-docker/stress_test report so existing dashboards can be
// reused without changes.
func formatReport(in reportInput) string {
	rate := 0.0
	if in.TotalGenerated > 0 {
		rate = float64(in.Stat.TotalTx) / float64(in.TotalGenerated)
	}
	span := int64(0)
	if in.Stat != nil {
		span = in.Stat.EndTimestamp - in.Stat.StartTimestamp
	}
	minutes := float64(span) / 1000.0 / 60.0
	tps := 0.0
	if span > 0 {
		tps = float64(in.Stat.TotalTx) * 1000.0 / float64(span)
	}
	missRate := 0.0
	if in.Stat != nil && in.Stat.EndBlock-in.Stat.StartBlock+1 > 0 {
		missRate = float64(in.Stat.EmptyBlocks) / float64(in.Stat.EndBlock-in.Stat.StartBlock+1)
	}
	return fmt.Sprintf(`Stress test report:
broadcast tps limit:        %d
statistic block range:      startBlock: %d, endBlock: %d
total generate tx count:    %d
total broadcast ok:         %d
total broadcast fail:       %d
tx on chain rate:           %f
cost time:                  %f minutes
max block size:             %d
min block size:             %d
tps:                        %f
miss block rate:            %f
`,
		in.TpsLimit,
		in.Stat.StartBlock, in.Stat.EndBlock,
		in.TotalGenerated,
		in.TotalBroadcastOK,
		in.TotalBroadcastFail,
		rate,
		minutes,
		in.Stat.MaxBlockSize,
		in.Stat.MinBlockSize,
		tps,
		missRate,
	)
}
