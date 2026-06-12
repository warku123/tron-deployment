package main

import (
	"strings"
	"testing"
)

const testPrivKeyHex = "0101010101010101010101010101010101010101010101010101010101010101"

func newPQConfig(ratio int, withPrivKey bool) *Config {
	c := &Config{}
	c.Generate.TxType.Transfer = 100
	c.Generate.TotalTxCount = 1
	c.Generate.ReceiverAddressCount = 1
	c.Generate.PQ.Enabled = true
	c.Generate.PQ.Scheme = SchemeMLDSA44
	c.Generate.PQ.Seed = testSeedHex
	c.Generate.PQ.Ratio = ratio
	if withPrivKey {
		c.Generate.PrivateKey = testPrivKeyHex
	}
	return c
}

func TestSignerSetPick(t *testing.T) {
	ecdsa := &signer{senderHex: "ec"}
	pq := &signer{senderHex: "pq"}

	mixed := &signerSet{ecdsa: ecdsa, pq: pq, pqRatio: 30}
	if mixed.pick(0) != pq || mixed.pick(29) != pq {
		t.Fatal("rolls below ratio should pick pq")
	}
	if mixed.pick(30) != ecdsa || mixed.pick(99) != ecdsa {
		t.Fatal("rolls at/above ratio should pick ecdsa")
	}

	allPQ := &signerSet{pq: pq, pqRatio: 100}
	if allPQ.pick(0) != pq || allPQ.pick(99) != pq {
		t.Fatal("ratio 100 should always pick pq")
	}

	noPQ := &signerSet{ecdsa: ecdsa, pqRatio: 0}
	if noPQ.pick(0) != ecdsa || noPQ.pick(99) != ecdsa {
		t.Fatal("disabled pq should always pick ecdsa")
	}
}

func TestBuildSignersMixed(t *testing.T) {
	set, err := buildSigners(newPQConfig(40, true))
	if err != nil {
		t.Fatal(err)
	}
	if set.pq == nil || set.ecdsa == nil {
		t.Fatal("mixed ratio should build both signers")
	}
	if set.pqRatio != 40 {
		t.Fatalf("pqRatio = %d, want 40", set.pqRatio)
	}
	if set.pq.senderHex == set.ecdsa.senderHex {
		t.Fatal("pq and ecdsa senders should differ")
	}

	// ratio 100 → no ECDSA signer needed.
	full, err := buildSigners(newPQConfig(100, false))
	if err != nil {
		t.Fatal(err)
	}
	if full.ecdsa != nil {
		t.Fatal("ratio 100 should not build an ecdsa signer")
	}
	if full.pq == nil {
		t.Fatal("ratio 100 should build a pq signer")
	}
}

func TestValidatePQRatio(t *testing.T) {
	// ratio omitted → defaults to 100; no privateKey required.
	c := newPQConfig(0, false)
	c.applyDefaults()
	if c.Generate.PQ.Ratio != 100 {
		t.Fatalf("omitted ratio should default to 100, got %d", c.Generate.PQ.Ratio)
	}
	if err := c.validate(); err != nil {
		t.Fatalf("ratio 100 without privateKey should be valid: %v", err)
	}

	// ratio < 100 without a privateKey is invalid (ECDSA remainder needs it).
	c = newPQConfig(50, false)
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("ratio < 100 without privateKey should fail")
	}

	// ratio < 100 with a privateKey is valid.
	c = newPQConfig(50, true)
	c.applyDefaults()
	if err := c.validate(); err != nil {
		t.Fatalf("ratio 50 with privateKey should be valid: %v", err)
	}

	// out-of-range ratio is rejected.
	c = newPQConfig(150, true)
	c.applyDefaults()
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "ratio") {
		t.Fatalf("ratio 150 should fail with a ratio error, got %v", err)
	}
}
