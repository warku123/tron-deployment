// Package guard holds the cross-cutting "require private network" safety
// predicate. It lives in internal/ (not cmd/) on purpose: both cmd/ (root
// mutators) and cmd/network/ need it, and a helper in cmd/ could not be
// imported by cmd/network/ without an import cycle.
//
// The gate is the C1 safety boundary extended to every mutating verb: an
// unattended agent can assert "I will only ever touch a private rig" and
// trond machine-enforces it.
//
//	requested? ──no──> allow (gate off)
//	   │ yes
//	   ▼
//	IsPrivate(network)? ──yes──> allow
//	   │ no
//	   ▼
//	PRIVATE_NETWORK_REQUIRED (exit 2)
package guard

import (
	"fmt"
	"os"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
)

// FlagValue is bound to the persistent --require-private flag by cmd/root.go.
// It is a package var (rather than a getter) so the cobra flag can write it
// directly and both cmd/ and cmd/network/ can read it via Requested().
var FlagValue bool

// EnvVar is the environment variable that also turns the gate on. An agent
// exports it once at session start so every mutating call is gated without
// per-command discipline.
const EnvVar = "TROND_REQUIRE_PRIVATE"

// Requested reports whether the private-network gate is on. Semantics are a
// one-way SAFETY FLOOR, NOT a --state-dir-style fallback: the flag OR a
// truthy env turns it on, and once on it cannot be turned off for the
// invocation. That is deliberate — the whole point of the gate is that an
// unattended agent can neither forget to set it nor accidentally (or via a
// compromised caller) disable it mid-session. `--require-private=false`
// therefore cannot override a truthy env.
func Requested() bool {
	return FlagValue || envTruthy(os.Getenv(EnvVar))
}

// envTruthy treats 1/true/yes/on (any case, trimmed) as true; everything
// else, including empty, as false.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "t":
		return true
	default:
		return false
	}
}

// Enforce returns a PRIVATE_NETWORK_REQUIRED error (exit 2) when the gate is
// requested and the given network is not private; otherwise nil. network is
// the value recorded in state (state.ManagedNode.Network) or the intent's
// network. An empty network (a node deployed before network tracking) is
// treated as not-private (fail-safe) with a message that points at the fix.
//
// Callers must run Enforce on state-only data BEFORE any target resolution
// (which can fail with TARGET_UNREACHABLE and mask this gate) and before any
// HUMAN_REQUIRED confirmation gate, so the agent learns "not private" first.
func Enforce(network string) error {
	return EnforceArg(false, network)
}

// NodeRef is a (name, network) pair for the multi-node guard.
type NodeRef struct {
	Name    string
	Network string
}

// EnforceNodes is the multi-node analog of Enforce for verbs that touch
// more than one node (chaos disconnect/partition, network destroy/upgrade).
// When the gate is on it refuses if ANY node is non-private, naming the
// first offender so the operator knows which node broke the guarantee. The
// caller must pass every node the operation will mutate, gathered from
// state, so the refusal happens before any partial mutation.
func EnforceNodes(refs []NodeRef) error {
	if !Requested() {
		return nil
	}
	for _, r := range refs {
		if intent.IsPrivate(r.Network) {
			continue
		}
		net := r.Network
		if net == "" {
			net = "<unrecorded>"
		}
		return output.NewError("PRIVATE_NETWORK_REQUIRED", output.ExitValidationError,
			fmt.Sprintf("--require-private is set but node %q is on network %q (not private); refusing the operation", r.Name, net)).
			WithSuggestions(
				"Every node the operation touches must be private",
				"Target a private network, or drop --require-private / "+EnvVar,
			)
	}
	return nil
}

// EnforceArg is Enforce plus an explicit per-call request, OR-ed with the
// flag/env floor. Callers that carry their own opt-in signal (e.g. the MCP
// apply tool's require_private argument, where there is no cobra flag) pass
// it here; requested=true forces the gate on for this call even if the
// global flag/env are off. CLI mutators use Enforce (requested=false).
func EnforceArg(requested bool, network string) error {
	if !(requested || Requested()) || intent.IsPrivate(network) {
		return nil
	}
	if network == "" {
		return output.NewError("PRIVATE_NETWORK_REQUIRED", output.ExitValidationError,
			"--require-private is set but this node has no recorded network "+
				"(it was deployed before network tracking)").
			WithSuggestions(
				"Re-apply the node so its network is recorded in state, then retry",
				"Or unset --require-private / "+EnvVar+" if you accept operating on it",
			)
	}
	return output.NewError("PRIVATE_NETWORK_REQUIRED", output.ExitValidationError,
		fmt.Sprintf("--require-private is set but this node's network is %q (not private); refusing the operation", network)).
		WithSuggestions(
			"Target a private-network node, or drop --require-private / " + EnvVar,
		)
}
