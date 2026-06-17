// Package snapshot wires the `trond snapshot` subcommand tree.
//
// The actual networking + tar logic lives in internal/snapshot so it can
// be reused (apply --snapshot, e2e test fixtures) without dragging cobra
// in. This package is the thin presentation layer.
package snapshot

import "github.com/spf13/cobra"

// Cmd is the parent command, attached to root by cmd/root.go.
var Cmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Download and manage java-tron chain database snapshots",
	Long: `Download official chain database snapshots so a new node can start
caught-up rather than syncing from genesis (which takes days on mainnet).

Snapshots are streamed: trond pipes the tarball through gunzip and tar
without ever writing the .tgz to disk. Disk-space and existing-database
checks run before any bytes hit the network.

  trond snapshot sources                         # list mirrors
  trond snapshot list --network mainnet          # show available backups
  trond snapshot download --network mainnet      # latest lite snapshot
  trond snapshot download --network mainnet --type full --region america
  trond snapshot download --network nile --to ./output-directory`,
}

func init() {
	Cmd.AddCommand(sourcesCmd)
	Cmd.AddCommand(listCmd)
	Cmd.AddCommand(downloadCmd)
	Cmd.AddCommand(jobsCmd)
	Cmd.AddCommand(logsCmd)
	Cmd.AddCommand(stopCmd)
	Cmd.AddCommand(pruneCmd)
	Cmd.AddCommand(cloneCmd)
}
