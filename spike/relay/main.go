// Command spike-relay is the throwaway U1 listener. It joins the tailnet as
// its own node (tsnet) or binds a host tailnet IP (-listen), holds requests
// open for a caller-chosen time, and reports which tailnet node WhoIs says
// each caller is. It exists to answer three questions before the real relay
// is built: can each agent hold a long-poll to it, for how long, and can the
// relay tell the agents apart by tailnet identity.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"
)

type whoIser interface {
	WhoIs(ctx context.Context, remoteAddr string) (identity, error)
}

type identity struct {
	Node   string   `json:"node"`
	Tags   []string `json:"tags,omitempty"`
	User   string   `json:"user,omitempty"`
	NodeID string   `json:"node_id,omitempty"`
}

type localWhoIs struct{ lc *local.Client }

func (w localWhoIs) WhoIs(ctx context.Context, addr string) (identity, error) {
	res, err := w.lc.WhoIs(ctx, addr)
	if err != nil {
		return identity{}, err
	}
	id := identity{}
	if res.Node != nil {
		id.Node = strings.TrimSuffix(res.Node.Name, ".")
		id.Tags = res.Node.Tags
		id.NodeID = string(res.Node.StableID)
	}
	if res.UserProfile != nil {
		id.User = res.UserProfile.LoginName
	}
	return id, nil
}

func main() {
	hostname := flag.String("hostname", "tincan-spike", "tsnet node name")
	dir := flag.String("dir", "", "tsnet state dir (default: OS config dir)")
	port := flag.Int("port", 8080, "port to serve on")
	listen := flag.String("listen", "", "bind this host tailnet IP instead of starting tsnet (uses host tailscaled)")
	flag.Parse()

	var ln net.Listener
	var who whoIser
	var err error
	if *listen != "" {
		if !strings.HasPrefix(*listen, "100.") {
			log.Fatalf("-listen must be a tailnet 100.x address, got %q", *listen)
		}
		ln, err = net.Listen("tcp", net.JoinHostPort(*listen, strconv.Itoa(*port)))
		who = localWhoIs{lc: &local.Client{}}
	} else {
		srv := &tsnet.Server{Hostname: *hostname, Dir: *dir, AuthKey: os.Getenv("TS_AUTHKEY")}
		defer srv.Close()
		ln, err = srv.Listen("tcp", ":"+strconv.Itoa(*port))
		if err == nil {
			var lc *local.Client
			lc, err = srv.LocalClient()
			who = localWhoIs{lc: lc}
		}
	}
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("spike-relay serving on %s", ln.Addr())
	log.Fatal(http.Serve(ln, handler(who)))
}

func handler(who whoIser) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/whois", func(w http.ResponseWriter, r *http.Request) {
		id, err := who.WhoIs(r.Context(), r.RemoteAddr)
		reply(w, r, id, err, 0)
	})
	mux.HandleFunc("/hold", func(w http.ResponseWriter, r *http.Request) {
		secs, _ := strconv.Atoi(r.URL.Query().Get("secs"))
		if secs < 0 || secs > 600 {
			http.Error(w, "secs must be 0..600", http.StatusBadRequest)
			return
		}
		id, err := who.WhoIs(r.Context(), r.RemoteAddr)
		start := time.Now()
		select {
		case <-time.After(time.Duration(secs) * time.Second):
		case <-r.Context().Done():
			log.Printf("hold %ds from %s (%s) dropped after %s", secs, id.Node, r.RemoteAddr, time.Since(start).Round(time.Second))
			return
		}
		reply(w, r, id, err, secs)
	})
	return mux
}

func reply(w http.ResponseWriter, r *http.Request, id identity, err error, held int) {
	out := map[string]any{"remote_addr": r.RemoteAddr, "held_secs": held, "identity": id}
	if err != nil {
		out["whois_error"] = err.Error()
	}
	log.Printf("%s %s -> node=%q user=%q tags=%v held=%ds err=%v", r.Method, r.URL.Path, id.Node, id.User, id.Tags, held, err)
	w.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(w).Encode(out); encErr != nil {
		fmt.Fprintln(os.Stderr, encErr)
	}
}
