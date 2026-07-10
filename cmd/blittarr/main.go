// Command blittarr is the BlitterAmp backend service.
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/BlitterAmp/Blittarr/internal/httpserver"
)

func main() {
	listen := flag.String("listen", envOr("BLITTARR_LISTEN", "127.0.0.1:8484"), "address to listen on")
	flag.Parse()

	srv := httpserver.New(*listen)
	slog.Info("blittarr listening", "addr", *listen, "docs", "http://"+*listen+"/docs/")
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
