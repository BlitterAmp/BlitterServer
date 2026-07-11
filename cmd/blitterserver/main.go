// Command blitterserver is the BlitterAmp backend service.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BlitterAmp/BlitterServer/internal/config"
	"github.com/BlitterAmp/BlitterServer/internal/httpserver"
	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/logging"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "blitterserver:", err)
		os.Exit(1)
	}
}

func run() error {
	// Pre-scan only for --config; config.Load owns the rest of the flags.
	// The pre-scan set must mirror every flag config.Load defines — flag.Parse
	// stops at the first unknown flag, which would hide a later --config.
	cfgPath := os.Getenv("BLITTER_CONFIG")
	args := os.Args[1:]
	pre := flag.NewFlagSet("pre", flag.ContinueOnError)
	pre.SetOutput(io.Discard)
	pre.String("listen", "", "")
	pre.String("data-dir", "", "")
	pre.String("log-level", "", "")
	cp := pre.String("config", cfgPath, "path to blitterserver.yaml")
	_ = pre.Parse(args)
	cfgPath = *cp

	// Strip --config from args before handing to config.Load.
	filtered := stripConfigArgs(args)

	cfg, err := config.Load(cfgPath, filtered, os.Getenv)
	if err != nil {
		return err
	}
	cfg.Log.FilePath = cfg.LogFilePathOrDefault()

	log, err := logging.Setup(cfg.Log)
	if err != nil {
		return err
	}

	st, err := store.Open(context.Background(), cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	mgr := library.NewManager(st, cfg.DataDir)
	srv := httpserver.New(cfg.Listen, st, mgr, cfg.DataDir, version)
	log.Info("blitterserver listening",
		"addr", cfg.Listen, "version", version, "data_dir", cfg.DataDir,
		"docs", "http://"+cfg.Listen+"/docs/")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
