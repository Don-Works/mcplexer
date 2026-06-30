package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mcplexer: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Parse subcommand from os.Args
	subcmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		subcmd = args[0]
		args = args[1:]
	}

	switch subcmd {
	case "serve":
		return cmdServe(args)
	case "connect":
		return cmdConnect(args)
	case "init":
		return cmdInit()
	case "status":
		return cmdStatus()
	case "version":
		return cmdVersion(args)
	case "dry-run":
		return cmdDryRun(args)
	case "secret":
		return cmdSecret(args)
	case "skill":
		return cmdSkill(args)
	case "migrate-skills":
		return cmdMigrateSkills(args)
	case "migrate-commands":
		return cmdMigrateCommands(args)
	case "migrate-ledger":
		return cmdMigrateLedger(args)
	case "memory":
		return cmdMemory(args)
	case "mesh":
		return cmdMesh(args)
	case "daemon":
		return cmdDaemon(args)
	case "setup":
		return cmdSetup()
	case "control-server":
		return cmdControlServer()
	case "run-job":
		return cmdRunJob(args)
	case "rules":
		return cmdRules(args)
	case "harness":
		return cmdHarness(args)
	case "scrub-audit":
		return cmdScrubAudit(args)
	case "config":
		return cmdConfig(args)
	case "doctor":
		return cmdDoctor(args)
	case "brw":
		return cmdBrw(args)
	default:
		return fmt.Errorf("unknown command: %s\nUsage: mcplexer [serve|connect|init|status|version|dry-run|secret|skill|migrate-skills|migrate-commands|migrate-ledger|memory|mesh|daemon|setup|control-server|run-job|rules|harness|scrub-audit|config|doctor|brw]", subcmd)
	}
}
