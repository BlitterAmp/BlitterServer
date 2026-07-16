package main

import (
	"context"
	"fmt"

	"github.com/BlitterAmp/BlitterServer/internal/library"
	"github.com/BlitterAmp/BlitterServer/internal/store"
)

func bootstrapFromEnvironment(ctx context.Context, st *store.Store, mgr *library.Manager, getenv func(string) string) error {
	if password := getenv("BLITTER_ADMIN_PASSWORD"); password != "" {
		complete, err := st.SetupComplete(ctx)
		if err != nil {
			return fmt.Errorf("check admin bootstrap state: %w", err)
		}
		if !complete {
			if _, err := st.InitializeAdminPassword(ctx, password); err != nil {
				return fmt.Errorf("BLITTER_ADMIN_PASSWORD is invalid: %w", err)
			}
		}
	}

	if musicDir := getenv("BLITTER_MUSIC_DIR"); musicDir != "" {
		kind, _, err := st.GetSetting(ctx, "source_kind")
		if err != nil {
			return fmt.Errorf("check music source bootstrap state: %w", err)
		}
		if kind == "" || kind == "none" {
			if err := mgr.Configure(ctx, musicDir); err != nil {
				return fmt.Errorf("BLITTER_MUSIC_DIR is invalid: %w", err)
			}
		}
	}
	return nil
}
