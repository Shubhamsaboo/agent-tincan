package cli

import (
	"github.com/spf13/cobra"
)

func connectCmd() *cobra.Command {
	var socket, relayURL string
	cmd := &cobra.Command{
		Use:   "connect <name>",
		Short: "Connect a cloud agent that cannot join the tailnet (ChatGPT) through the gateway (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := adminRelay(socket, relayURL)
			if err != nil {
				return err
			}
			var out struct {
				Code string `json:"code"`
				URL  string `json:"url"`
			}
			if err := r.Raw(cmd.Context(), "POST", "/v1/admin/connect", map[string]string{"name": args[0]}, &out); err != nil {
				return err
			}
			cmd.Printf(`%q is ready to connect.
1. In ChatGPT: Settings > Apps > Advanced settings, turn on Developer mode.
2. Create a connector with this URL:
     %s
3. When ChatGPT opens the login page, enter this one-time code (valid 10 minutes):
     %s
`, args[0], out.URL, out.Code)
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "relay admin socket (when running on the relay host)")
	cmd.Flags().StringVar(&relayURL, "relay", "", "relay URL (default: saved config)")
	return cmd
}
