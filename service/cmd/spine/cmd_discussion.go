package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func discussionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "discussion",
		Aliases: []string{"discuss"},
		Short:   "Manage discussion threads",
	}

	cmd.AddCommand(discussionListCmd())
	cmd.AddCommand(discussionShowCmd())
	cmd.AddCommand(discussionCommentCmd())
	cmd.AddCommand(discussionResolveCmd())

	return cmd
}

func discussionListCmd() *cobra.Command {
	var artifact, anchorType, anchorID, status string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List discussion threads",
		RunE: func(cmd *cobra.Command, args []string) error {
			// --artifact is a convenience flag that sets anchor_type=artifact
			if artifact != "" {
				anchorType = "artifact"
				anchorID = artifact
			}
			if anchorType == "" || anchorID == "" {
				return fmt.Errorf("--artifact PATH or both --anchor-type and --anchor-id are required")
			}

			params := url.Values{}
			params.Set("anchor_type", anchorType)
			params.Set("anchor_id", anchorID)
			if status != "" {
				params.Set("status", status)
			}

			c := newAPIClient()
			data, err := c.Get(context.Background(), "/api/v1/discussions", params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&artifact, "artifact", "", "Filter by artifact path (shorthand for --anchor-type artifact --anchor-id PATH)")
	cmd.Flags().StringVar(&anchorType, "anchor-type", "", "Anchor type (artifact, run, step_execution, divergence_context)")
	cmd.Flags().StringVar(&anchorID, "anchor-id", "", "Anchor ID")
	cmd.Flags().StringVar(&status, "status", "", "Filter by thread status (open, resolved, archived)")
	return cmd
}

func discussionShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <thread-id>",
		Short: "Show a discussion thread with comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := newAPIClient()
			data, err := c.Get(context.Background(), "/api/v1/discussions/"+args[0], nil)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
}

func discussionCommentCmd() *cobra.Command {
	var parentID string

	cmd := &cobra.Command{
		Use:   "comment <thread-id> <message>",
		Short: "Add a comment to a discussion thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			threadID := args[0]
			message := args[1]

			body := map[string]string{"content": message}
			if parentID != "" {
				body["parent_comment_id"] = parentID
			}

			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/discussions/"+threadID+"/comments", body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&parentID, "parent", "", "Parent comment ID for threaded reply")
	return cmd
}

func discussionResolveCmd() *cobra.Command {
	var resolutionType string
	var resolutionRefs []string

	cmd := &cobra.Command{
		Use:   "resolve <thread-id>",
		Short: "Resolve a discussion thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if resolutionType != "" {
				body["resolution_type"] = resolutionType
			}
			if len(resolutionRefs) > 0 {
				body["resolution_refs"] = resolutionRefs
			}

			c := newAPIClient()
			data, err := c.Post(context.Background(), "/api/v1/discussions/"+args[0]+"/resolve", body)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}

	cmd.Flags().StringVar(&resolutionType, "type", "", "Resolution type (artifact_updated, artifact_created, adr_created, decision_recorded, no_action)")
	cmd.Flags().StringSliceVar(&resolutionRefs, "ref", nil, "Resolution reference (can be repeated)")
	return cmd
}
