// Package config loads Blittarr's bootstrap configuration. Runtime settings
// live in SQLite behind the admin API; this is only what the process needs
// before the store exists.
package config

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BlitterAmp/Blittarr/internal/logging"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen  string          `yaml:"listen"`
	DataDir string          `yaml:"data_dir"`
	Log     logging.Options `yaml:"-"`
}

// fileConfig mirrors the YAML shape (logging.Options carries no yaml tags).
type fileConfig struct {
	Listen  string `yaml:"listen"`
	DataDir string `yaml:"data_dir"`
	Log     struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
		File   struct {
			Enabled    *bool  `yaml:"enabled"`
			Path       string `yaml:"path"`
			MaxSizeMB  int    `yaml:"max_size_mb"`
			MaxBackups int    `yaml:"max_backups"`
			MaxAgeDays int    `yaml:"max_age_days"`
			Compress   *bool  `yaml:"compress"`
		} `yaml:"file"`
	} `yaml:"log"`
}

// Load applies precedence flags > env > file > defaults. path="" means no
// config file; a missing file at an explicit path is an error, as is
// malformed YAML.
func Load(path string, args []string, getenv func(string) string) (Config, error) {
	c := Config{
		Listen: "127.0.0.1:8484",
		Log: logging.Options{
			Level: "info", Format: "text", FileEnabled: true,
			MaxSizeMB: 10, MaxBackups: 5, MaxAgeDays: 30, Compress: true,
		},
	}
	c.DataDir = defaultDataDir(getenv)

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("config file: %w", err)
		}
		var fc fileConfig
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("config file %s: %w", path, err)
		}
		apply(&c, fc)
	}

	if v := getenv("BLITTARR_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := getenv("BLITTARR_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := getenv("BLITTARR_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := getenv("BLITTARR_LOG_FORMAT"); v != "" {
		c.Log.Format = v
	}

	fs := flag.NewFlagSet("blittarr", flag.ContinueOnError)
	listen := fs.String("listen", c.Listen, "address to listen on")
	dataDir := fs.String("data-dir", c.DataDir, "state directory (sqlite, caches, logs)")
	logLevel := fs.String("log-level", c.Log.Level, "debug|info|warn|error")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	c.Listen, c.DataDir, c.Log.Level = *listen, *dataDir, *logLevel
	return c, nil
}

// LogFilePathOrDefault resolves the file sink path, defaulting under DataDir.
func (c Config) LogFilePathOrDefault() string {
	if c.Log.FilePath != "" {
		return c.Log.FilePath
	}
	return filepath.Join(c.DataDir, "logs", "blittarr.log")
}

func defaultDataDir(getenv func(string) string) string {
	if x := getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "blittarr")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "blittarr-data"
	}
	return filepath.Join(home, ".local", "share", "blittarr")
}

func apply(c *Config, fc fileConfig) {
	if fc.Listen != "" {
		c.Listen = fc.Listen
	}
	if fc.DataDir != "" {
		c.DataDir = fc.DataDir
	}
	if fc.Log.Level != "" {
		c.Log.Level = fc.Log.Level
	}
	if fc.Log.Format != "" {
		c.Log.Format = fc.Log.Format
	}
	if fc.Log.File.Enabled != nil {
		c.Log.FileEnabled = *fc.Log.File.Enabled
	}
	if fc.Log.File.Path != "" {
		c.Log.FilePath = fc.Log.File.Path
	}
	if fc.Log.File.MaxSizeMB > 0 {
		c.Log.MaxSizeMB = fc.Log.File.MaxSizeMB
	}
	if fc.Log.File.MaxBackups > 0 {
		c.Log.MaxBackups = fc.Log.File.MaxBackups
	}
	if fc.Log.File.MaxAgeDays > 0 {
		c.Log.MaxAgeDays = fc.Log.File.MaxAgeDays
	}
	if fc.Log.File.Compress != nil {
		c.Log.Compress = *fc.Log.File.Compress
	}
}
