package main

import "fmt"

func cmdConfig(args []string) error {
	subcmd := "show"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		subcmd = args[0]
		args = args[1:]
	}

	switch subcmd {
	case "show":
		return cmdConfigShow(args)
	case "describe":
		return cmdConfigDescribe(args)
	case "export":
		return cmdConfigExport(args)
	case "import":
		return cmdConfigImport(args)
	default:
		return fmt.Errorf("unknown config subcommand: %s\nUsage: mcplexer config [show|describe|export|import <file>]", subcmd)
	}
}
