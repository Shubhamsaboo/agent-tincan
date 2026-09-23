package cli

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/mvanhorn/agent-tincan/internal/mcpserver"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the Agent Tincan MCP server over stdio (add this to your agent's MCP config)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, _, err := connect()
			if err != nil {
				return err
			}
			return mcpserver.New(r, Version).Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}
