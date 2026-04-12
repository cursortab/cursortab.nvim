// cursortab-eval runs the cursortab evaluation harness.
//
// Two subcommands:
//
//	record   Hit real provider APIs and capture responses as cassettes.
//	run      Replay cassettes through the engine and emit quality reports.
//
// Cassettes live inside each scenario .txtar under the cassette/<provider>.ndjson
// section. Recording is the only time this tool touches the network — run is
// fully offline and deterministic.
package main

import (
	"fmt"
	"os"

	"cursortab/logger"
)

func main() {
	// Suppress engine lifecycle logs — the eval harness creates/destroys
	// hundreds of engines and the start/stop noise drowns the report.
	logger.SetLevel(logger.LogLevelError)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	var err error
	switch sub {
	case "run":
		err = runCmd(args)
	case "record":
		err = recordCmd(args)
	case "record-copilot":
		err = recordCopilotCmd(args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", sub)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `cursortab-eval - cursortab provider evaluation harness

USAGE:
  cursortab-eval <command> [flags]

COMMANDS:
  run              Replay cassettes and emit quality reports
  record           Hit real provider APIs and capture cassettes (HTTP targets)
  record-copilot   Drive an external nvim session to capture Copilot NES cassettes
  help             Show this help

Run "cursortab-eval <command> --help" for command-specific flags.`)
}
