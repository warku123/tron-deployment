package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	configCmd "github.com/tronprotocol/tron-deployment/cmd/config"
	networkCmd "github.com/tronprotocol/tron-deployment/cmd/network"
	shadowforkCmd "github.com/tronprotocol/tron-deployment/cmd/shadowfork"
	snapshotCmd "github.com/tronprotocol/tron-deployment/cmd/snapshot"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
)

var (
	// Global flags
	outputFormat string
	logFormat    string
	quiet        bool
	verbose      bool
	noColor      bool
	configFile   string
	stateDirFlag string
)

// version, commit and buildTime are populated at link time via -ldflags by
// the Makefile and goreleaser. Defaults to "dev" so unstamped local builds
// still report something coherent.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "trond",
	Short: "TRON node deployment and lifecycle management",
	Long: `trond is a CLI tool for deploying and managing java-tron nodes.

It uses declarative intent files to describe desired node state,
then renders configuration and deploys via Docker or native jar+systemd.

Supports local and remote (SSH) targets with structured JSON output
for CI pipelines and AI agents.`,
	Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildTime),
	SilenceUsage:  true,
	SilenceErrors: true,
	// Apply --state-dir before any subcommand runs so subpackages
	// (cmd/network, cmd/config) see the same base.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if stateDirFlag != "" {
			paths.SetBaseDir(stateDirFlag)
		}
	},
}

func init() {
	rootCmd.AddCommand(configCmd.Cmd)
	rootCmd.AddCommand(networkCmd.Cmd)
	rootCmd.AddCommand(shadowforkCmd.Cmd)
	rootCmd.AddCommand(snapshotCmd.Cmd)

	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", "Output format: text, json")
	rootCmd.PersistentFlags().StringVar(&logFormat, "log-format", "text", "Log format: text, json")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress non-essential output")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Increase log verbosity")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable ANSI colors")
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "Config file (default ~/.trond/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&stateDirFlag, "state-dir", "", "Directory for state.json, audit.log, deployments (default ~/.trond, env: TROND_STATE_DIR)")
}

// Root returns the configured root cobra command, used by the doc/manpage
// generator under cmd/gendoc and by tests that want to walk the tree.
func Root() *cobra.Command { return rootCmd }

// Version returns the build-stamped version string set via -ldflags.
// Exposed for callers (main.go) that need it before cobra parses
// arguments — e.g. the OpenTelemetry resource tag.
func Version() string { return version }

// Execute runs the root command and returns the exit code.
// StructuredError values are rendered in the requested format and their
// ExitCode is returned. This runs after cobra has unwound all RunE deferred
// cleanup (state locks, SSH sessions, etc.), so exiting here is safe.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		var sErr *output.StructuredError
		if errors.As(err, &sErr) {
			output.WriteError(os.Stderr, sErr, outputFormat)
			return sErr.ExitCode
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// OutputFormat returns the current output format flag value.
func OutputFormat() string {
	return outputFormat
}

// IsQuiet returns whether quiet mode is enabled.
func IsQuiet() bool {
	return quiet
}

// IsVerbose returns whether verbose mode is enabled.
func IsVerbose() bool {
	return verbose
}

// NoColor returns whether color output is disabled.
func NoColor() bool {
	return noColor
}

// Log returns a structured logger configured from global flags.
func Log() *output.Logger {
	return output.NewLogger(os.Stderr, logFormat == "json", verbose, quiet)
}

// mustMarkRequired marks a flag as required and panics if the flag does not
// exist. Failure is only possible at program start due to a programming
// error, so panicking is the right behavior — it fails loudly at init().
func mustMarkRequired(cmd *cobra.Command, name string) {
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(fmt.Sprintf("mark flag %q required on %s: %v", name, cmd.Name(), err))
	}
}
