package main

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

func run(ctx context.Context, args []string, output io.Writer) error {
	root := newRootCommand()
	root.SetArgs(append([]string{}, args...))
	root.SetOut(output)
	root.SetErr(output)
	return root.ExecuteContext(ctx)
}

// Cobra validates flags and arguments before invoking a handler. Help and
// invalid commands never load configuration or connect to PostgreSQL.
func newRootCommand() *cobra.Command {
	serveCommand := &cobra.Command{
		Use: "serve", Short: "Run the API server", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context()) },
	}
	root := &cobra.Command{
		Use: "onlydemo", Short: "OnlyDemo creator channels with AuthKit and OpenRails",
		Long: "Run the API server or an explicit maintenance command. Configuration comes from environment variables.",
		Args: cobra.NoArgs, RunE: serveCommand.RunE,
		SilenceUsage: true, SilenceErrors: true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.AddCommand(serveCommand, &cobra.Command{
		Use: "migrate", Short: "Initialize application and library storage", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return migrate(cmd.Context()) },
	})
	admin := &cobra.Command{
		Use: "admin", Short: "Manage a registered user's AuthKit admin role", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("choose admin grant or admin revoke --user-id ID")
		},
	}
	admin.AddCommand(
		adminCommand("grant", "Grant the admin role", grantAdmin),
		adminCommand("revoke", "Revoke the admin role", revokeAdmin),
	)
	root.AddCommand(&cobra.Command{
		Use: "media-worker", Short: "Run the media worker: upload placement, image renditions, video encodes (needs libvips and ffmpeg)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runMediaWorker(cmd.Context()) },
	})
	root.AddCommand(admin, urlCommand("seed", "Create display channels and posts through a running server's routes", seed),
		urlCommand("smoke", "Walk a running server through a creator's and a fan's journeys", smoke))
	return root
}

// urlCommand runs action against a running server (--url).
func urlCommand(name, description string, action func(context.Context, string, io.Writer) error) *cobra.Command {
	var base string
	cmd := &cobra.Command{
		Use: name, Short: description, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return action(cmd.Context(), strings.TrimRight(base, "/"), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&base, "url", "http://127.0.0.1:3000", "running server base URL")
	return cmd
}

func adminCommand(name, description string, action func(context.Context, string) error) *cobra.Command {
	var userID string
	cmd := &cobra.Command{
		Use: name, Short: description,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("user-id") && strings.TrimSpace(userID) == "" {
				return errors.New("--user-id cannot be empty")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return action(cmd.Context(), strings.TrimSpace(userID))
		},
	}
	cmd.Flags().StringVar(&userID, "user-id", "", "registered AuthKit user ID")
	if err := cmd.MarkFlagRequired("user-id"); err != nil {
		panic(err) // The flag is declared above; absence is a programming error.
	}
	return cmd
}
