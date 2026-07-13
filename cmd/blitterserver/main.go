// Command blitterserver is the BlitterAmp backend service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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
	pre.Bool("reset-db-on-schema-mismatch", false, "")
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

	st, err := openStore(context.Background(), cfg.DataDir, cfg.ResetDBOnSchemaMismatch)
	if err != nil {
		return err
	}
	defer st.Close()

	mgr := library.NewManager(st, cfg.DataDir)
	srv := httpserver.New(cfg.Listen, st, mgr, cfg.DataDir, version)
	// Registered after the store defer, so workers always stop/join first even
	// when ListenAndServe fails before the signal-driven Shutdown path.
	defer srv.StopWorkers()
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

func openStore(ctx context.Context, dataDir string, resetOnMismatch bool) (*store.Store, error) {
	st, err := store.Open(ctx, dataDir)
	var mismatch *store.MigrationMismatchError
	if err == nil || !resetOnMismatch || !errors.As(err, &mismatch) {
		return st, err
	}
	dbPath := filepath.Join(dataDir, "blitterserver.db")
	preserved := fmt.Sprintf("%s.corrupt-%d", dbPath, time.Now().Unix())
	for i := int64(1); ; i++ {
		if _, statErr := os.Stat(preserved); errors.Is(statErr, os.ErrNotExist) {
			break
		} else if statErr != nil {
			return nil, fmt.Errorf("check preserved database path: %w", statErr)
		}
		preserved = fmt.Sprintf("%s.corrupt-%d", dbPath, time.Now().Unix()+i)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := dbPath + suffix
		if renameErr := os.Rename(source, preserved+suffix); renameErr != nil && !errors.Is(renameErr, os.ErrNotExist) {
			return nil, fmt.Errorf("preserve mismatched database %s: %w", source, renameErr)
		}
	}
	logging.From(ctx).Warn("moved aside database with migration mismatch",
		"migration", mismatch.Version, "preserved_path", preserved)
	return store.Open(ctx, dataDir)
}
