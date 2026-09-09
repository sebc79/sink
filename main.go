package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	storage := flag.String("storage", "storage", "storage directory (created if missing)")
	maxUpload := flag.String("max-upload", "512MB", "maximum upload size (e.g. 64MB, 1G)")
	maxExtract := flag.String("max-extract", "1GB", "maximum uncompressed archive size")
	maxFiles := flag.Int("max-files", defaultMaxFiles, "maximum files extracted from one archive")
	flag.Parse()

	up, err := parseSize(*maxUpload)
	if err != nil {
		fatal("max-upload: %v", err)
	}
	ex, err := parseSize(*maxExtract)
	if err != nil {
		fatal("max-extract: %v", err)
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := New(Config{
		StorageDir: *storage,
		MaxUpload:  up,
		MaxExtract: ex,
		MaxFiles:   *maxFiles,
		Logger:     log,
	})
	if err != nil {
		fatal("init: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("listen", "addr", *addr, "storage", srv.root)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal("listen: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "KIB"):
		mult, s = 1024, s[:len(s)-3]
	case strings.HasSuffix(upper, "MIB"):
		mult, s = 1024*1024, s[:len(s)-3]
	case strings.HasSuffix(upper, "GIB"):
		mult, s = 1024*1024*1024, s[:len(s)-3]
	case strings.HasSuffix(upper, "KB"):
		mult, s = 1024, s[:len(s)-2]
	case strings.HasSuffix(upper, "MB"):
		mult, s = 1024*1024, s[:len(s)-2]
	case strings.HasSuffix(upper, "GB"):
		mult, s = 1024*1024*1024, s[:len(s)-2]
	case strings.HasSuffix(upper, "K"):
		mult, s = 1024, s[:len(s)-1]
	case strings.HasSuffix(upper, "M"):
		mult, s = 1024*1024, s[:len(s)-1]
	case strings.HasSuffix(upper, "G"):
		mult, s = 1024*1024*1024, s[:len(s)-1]
	case strings.HasSuffix(upper, "B"):
		mult, s = 1, s[:len(s)-1]
	}
	s = strings.TrimSpace(s)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	return n * mult, nil
}
