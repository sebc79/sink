package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	tree := flag.String("tree", "", "ArborSync checkout to read (required)")
	peer := flag.String("peer-socket", "", "ArborSync peer socket")
	sender := flag.String("sender-slave", "grok-bot-box", "slave id to watch while a view is held")
	hold := flag.Duration("hold-timeout", 30*time.Second, "how long /view waits for a syncing file")
	rescan := flag.Duration("index-rescan", 45*time.Second, "how often to rescan -tree")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := New(Config{
		TreeDir:     *tree,
		PeerSocket:  *peer,
		SenderID:    *sender,
		HoldTimeout: *hold,
		IndexRescan: *rescan,
		Logger:      log,
	})
	if err != nil {
		fatal("init: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("listen", "addr", *addr, "tree", srv.tree)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal("listen: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
