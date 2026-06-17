package intent

// Network names trond understands in an intent's `network:` field.
const (
	NetworkMainnet = "mainnet"
	NetworkNile    = "nile"
	NetworkPrivate = "private"
)

// IsPrivate reports whether a network name denotes a private network —
// a self-contained chain with no real seed nodes, safe for an
// unattended agent to mutate (deploy / fire transactions / tear down).
//
// Only "private" qualifies. "mainnet" is production; "nile" is a public
// testnet — both are shared infrastructure an agent must never touch
// blindly. This is the predicate behind `status --json`'s `is_private`
// fact and `apply --require-private`, so an automated caller can PROVE a
// rig is private before acting rather than trusting a standing rule.
func IsPrivate(network string) bool {
	return network == NetworkPrivate
}
