package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"tailscale.com/tsnet"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/identity"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

type relayFlags struct {
	listen   string
	hostname string
	stateDir string
	port     int
	admins   []string
}

func relayCmd() *cobra.Command {
	var f relayFlags
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Run the relay (on the always-on machine, e.g. the Grok Bot VM)",
		Long: `Run the relay. By default it joins the tailnet as its own node with tsnet
(set TS_AUTHKEY, or follow the login URL it prints). With --listen it binds
this host's tailnet IP and uses the host's tailscaled instead.

Admin commands (invite, remove) are accepted from the machines named in
--admin and from the local admin socket in the state dir.`,
		RunE: func(cmd *cobra.Command, _ []string) error { return runRelay(cmd.Context(), f) },
	}
	cmd.Flags().StringVar(&f.listen, "listen", "", "bind this host tailnet IP (100.x.y.z) instead of starting tsnet")
	cmd.Flags().StringVar(&f.hostname, "hostname", "tincan-relay", "tsnet node name")
	cmd.Flags().StringVar(&f.stateDir, "state-dir", defaultStateDir(), "relay state: database, tsnet state, admin socket")
	cmd.Flags().IntVar(&f.port, "port", 80, "port to serve the agent API on")
	cmd.Flags().StringSliceVar(&f.admins, "admin", nil, "machine names allowed to run admin commands (e.g. macbook-pro-44,iphone182)")
	return cmd
}

func defaultStateDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "tincan-relay")
	}
	return ".tincan-relay"
}

func runRelay(ctx context.Context, f relayFlags) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := os.MkdirAll(f.stateDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(f.stateDir, "relay.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	var ln net.Listener
	var who *identity.LocalResolver
	if f.listen != "" {
		if !strings.HasPrefix(f.listen, "100.") {
			return fmt.Errorf("--listen must be a tailnet 100.x address, got %q", f.listen)
		}
		who = identity.NewLocalResolverAt("")
		if err := who.Probe(ctx); err != nil {
			return fmt.Errorf("refusing to start without WhoIs: %w", err)
		}
		ln, err = net.Listen("tcp", net.JoinHostPort(f.listen, fmt.Sprint(f.port)))
		if err != nil {
			return err
		}
	} else {
		ts := &tsnet.Server{Hostname: f.hostname, Dir: filepath.Join(f.stateDir, "tsnet"), AuthKey: os.Getenv("TS_AUTHKEY")}
		defer ts.Close()
		if _, err := ts.Up(ctx); err != nil {
			return fmt.Errorf("tsnet up: %w", err)
		}
		lc, err := ts.LocalClient()
		if err != nil {
			return err
		}
		who = identity.NewLocalResolver(lc)
		ln, err = ts.Listen("tcp", fmt.Sprintf(":%d", f.port))
		if err != nil {
			return err
		}
	}
	if len(f.admins) == 0 {
		log.Printf("no --admin machines set: invites only work from the local admin socket")
	}

	dir := identity.NewDirectory(st, who, identity.Config{Admins: f.admins})
	srv := relay.New(dir, st, relay.Config{})
	go srv.Run(ctx)

	api := client.Configure(&http.Server{Handler: srv.Handler()}, client.RelayAPI)
	adminSock := filepath.Join(f.stateDir, "admin.sock")
	os.Remove(adminSock)
	aln, err := net.Listen("unix", adminSock)
	if err != nil {
		return fmt.Errorf("admin socket: %w", err)
	}
	if err := os.Chmod(adminSock, 0o600); err != nil {
		return err
	}
	admin := client.Configure(&http.Server{Handler: srv.AdminHandler()}, client.RelayAPI)

	errc := make(chan error, 2)
	go func() { errc <- api.Serve(ln) }()
	go func() { errc <- admin.Serve(aln) }()
	log.Printf("tincan relay serving on %s (admin socket %s)", ln.Addr(), adminSock)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	api.Close()
	admin.Close()
	return nil
}
