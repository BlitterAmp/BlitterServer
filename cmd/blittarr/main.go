// Command blittarr is the BlitterAmp backend service.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/BlitterAmp/Blittarr/internal/config"
	"github.com/BlitterAmp/Blittarr/internal/httpserver"
	"github.com/BlitterAmp/Blittarr/internal/logging"
	"github.com/BlitterAmp/Blittarr/internal/store"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	cfg, err := config.Load(os.Getenv("BLITTARR_CONFIG"), os.Args[1:], os.Getenv)
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	if _, err := logging.Setup(withDefaultFilePath(cfg)); err != nil {
		slog.Error("logging setup failed", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(context.Background(), cfg.DataDir)
	if err != nil {
		slog.Error("store open failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	srv := httpserver.New(cfg.Listen, st, version)
	slog.Info("blittarr listening", "addr", cfg.Listen, "docs", "http://"+cfg.Listen+"/docs/")
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func withDefaultFilePath(cfg config.Config) logging.Options {
	o := cfg.Log
	o.FilePath = cfg.LogFilePathOrDefault()
	return o
}
