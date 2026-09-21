package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

type command struct {
	kind   string
	userID string
	revoke bool
}

const commandUsage = `Usage:
  openrails-demo [serve]
  openrails-demo migrate
  openrails-demo admin grant --user-id ID
  openrails-demo admin revoke --user-id ID
  openrails-demo help

Configuration comes from environment variables; one-off operations are commands.
`

// Parse before configuration or database access so help and invalid commands
// remain safe even when a configured database is unavailable.
func parseCommand(args []string, output io.Writer) (command, error) {
	if len(args) == 0 {
		return command{kind: "serve"}, nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		if len(args) != 1 {
			return command{}, errors.New("help accepts no arguments")
		}
		_, err := io.WriteString(output, commandUsage)
		return command{kind: "help"}, err
	case "serve", "migrate":
		if len(args) != 1 {
			return command{}, fmt.Errorf("%s accepts no arguments", args[0])
		}
		return command{kind: args[0]}, nil
	case "admin":
		if len(args) < 2 || (args[1] != "grant" && args[1] != "revoke") {
			return command{}, errors.New("use admin grant or admin revoke --user-id ID")
		}
		flags := flag.NewFlagSet("admin "+args[1], flag.ContinueOnError)
		flags.SetOutput(output)
		userID := flags.String("user-id", "", "registered AuthKit user ID")
		if err := flags.Parse(args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return command{kind: "help"}, nil
			}
			return command{}, err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*userID) == "" {
			return command{}, errors.New("admin command requires --user-id ID and no positional arguments")
		}
		return command{kind: "admin", userID: strings.TrimSpace(*userID), revoke: args[1] == "revoke"}, nil
	default:
		return command{}, fmt.Errorf("unknown command %q; use help", args[0])
	}
}
