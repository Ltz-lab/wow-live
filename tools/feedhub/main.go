// feedhub: the feed as a local WebSocket server. It sits at the end of a feed
// pipe, passes every line through to stdout unchanged, and serves the same
// lines to any number of WebSocket clients: overlays, dashboards, bots.
//
//	combatfeed -run <run> -follow ... | wow-rotation <toml> --feed | feedhub | auraoverlay.py --rotation
//
// Endpoints (default 127.0.0.1:7373):
//
//	ws://…/feed            every line as one text message, as {"v":1,"seq":N,...the line...}.
//	                       A new client first gets the current state (the latest line of each
//	                       type, per unit where a line has one; never "event"; a "player"
//	                       line, a login or relog, starts it afresh), then
//	                       {"v":1,"seq":N,"type":"hub","live":true} with the latest seq, then the stream.
//	ws://…/feed?types=auras,next   only those types
//	http://…/state         the current state as JSON lines
//	http://…/              a live view of the state in the browser
//
// After the "hub" line seq rises by one per line, so a client with no types
// filter that sees a gap knows it missed one. A client whose buffer fills is closed with
// 1008 "too slow" rather than slowing the feed. Browser pages may connect
// only from the hub's own origin or one matching -origin; -token, if set,
// must be passed as ?token=. When stdin ends, clients are closed with 1000
// "feed ended" and feedhub exits.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

//go:embed viewer.html
var viewer []byte

func main() {
	addr := flag.String("ws", "127.0.0.1:7373", "where to listen")
	origins := flag.String("origin", "localhost:*,127.0.0.1:*", "comma-separated host patterns browser pages may connect from (besides the hub's own)")
	token := flag.String("token", "", "if set, clients must pass ?token=<this>")
	queue := flag.Int("queue", 4096, "lines buffered per client before it is dropped as too slow")
	quiet := flag.Bool("no-stdout", false, "do not pass lines through to stdout")
	flag.Parse()
	log.SetPrefix("feedhub: ")
	log.SetFlags(0)

	// a closed stdout (the overlay quit) is an error to report, not the end:
	// the socket keeps serving
	signal.Ignore(syscall.SIGPIPE)

	h := newHub(*queue)
	srv := &http.Server{Handler: handler(h, splitList(*origins), *token)}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving ws://%s/feed (live view http://%s/)", ln.Addr(), ln.Addr())
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	var out io.Writer = os.Stdout
	if *quiet {
		out = io.Discard
	}
	if err := pump(os.Stdin, out, h); err != nil {
		log.Print(err)
	}
	h.closeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// pump copies stdin to out line by line (one write per line, so the next tool
// in the pipe sees each line as it comes) and publishes each line.
func pump(in io.Reader, out io.Writer, h *hub) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	stdoutOK := true
	for sc.Scan() {
		line := sc.Bytes()
		if stdoutOK {
			buf := make([]byte, len(line)+1)
			copy(buf, line)
			buf[len(line)] = '\n'
			if _, err := out.Write(buf); err != nil {
				// the reader went away (the overlay closed): keep serving the socket
				log.Printf("stdout: %v; serving the socket only", err)
				stdoutOK = false
			}
		}
		h.publish(append([]byte(nil), line...))
	}
	return sc.Err()
}

func handler(h *hub, origins []string, token string) http.Handler {
	authed := func(r *http.Request) bool { return token == "" || r.URL.Query().Get("token") == token }
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: origins})
		if err != nil {
			return // Accept has written the response
		}
		serve(r.Context(), conn, h, parseTypes(r.URL.Query().Get("types")))
	})
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		types := parseTypes(r.URL.Query().Get("types"))
		for _, s := range h.snapshot() {
			if types == nil || types[s.typ] {
				w.Write(s.line)
				w.Write([]byte{'\n'})
			}
		}
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(viewer)
	})
	return mux
}

// serve writes the client's lines to its socket until the feed ends, the
// client is dropped, or it goes away. Reads are discarded (CloseRead), which
// also notices the client closing.
func serve(ctx context.Context, conn *websocket.Conn, h *hub, types map[string]bool) {
	c := h.join(types)
	defer h.leave(c)
	ctx = conn.CloseRead(ctx)
	for {
		select {
		case line, ok := <-c.out:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "feed ended")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, line)
			cancel()
			if err != nil {
				conn.CloseNow()
				return
			}
		case <-c.gone:
			conn.Close(websocket.StatusPolicyViolation, "too slow")
			return
		case <-ctx.Done():
			conn.CloseNow()
			return
		}
	}
}

func parseTypes(s string) map[string]bool {
	list := splitList(s)
	if len(list) == 0 {
		return nil
	}
	m := map[string]bool{}
	for _, t := range list {
		m[t] = true
	}
	return m
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
