package config

import (
	"os"
	"path/filepath"
	"testing"
)

func noEnv(string) string { return "" }

func TestDefaults(t *testing.T) {
	c, err := Load("", nil, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8484" || c.Log.Level != "info" || !c.Log.FileEnabled || c.Log.MaxSizeMB != 10 {
		t.Fatalf("bad defaults: %+v", c)
	}
	if c.DataDir == "" {
		t.Fatal("DataDir default must resolve")
	}
}

func TestPrecedenceFlagsBeatEnvBeatFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "blittarr.yaml")
	os.WriteFile(file, []byte("listen: \"file:1\"\nlog:\n  level: warn\n"), 0o644)
	env := func(k string) string {
		if k == "BLITTARR_LISTEN" {
			return "env:2"
		}
		return ""
	}
	c, err := Load(file, []string{"--listen", "flag:3"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "flag:3" {
		t.Fatalf("flags must win: %q", c.Listen)
	}
	if c.Log.Level != "warn" {
		t.Fatalf("file value must apply when env/flag absent: %q", c.Log.Level)
	}
}

func TestXDGDataDir(t *testing.T) {
	env := func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/tmp/xdg"
		}
		return ""
	}
	c, _ := Load("", nil, env)
	if c.DataDir != "/tmp/xdg/blittarr" {
		t.Fatalf("want XDG_DATA_HOME/blittarr, got %q", c.DataDir)
	}
}

func TestMalformedFileIsError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blittarr.yaml")
	os.WriteFile(file, []byte(":\t not yaml"), 0o644)
	if _, err := Load(file, nil, noEnv); err == nil {
		t.Fatal("want error for malformed file")
	}
}

func TestLogFilePathDefaultsUnderDataDir(t *testing.T) {
	c, _ := Load("", nil, noEnv)
	want := filepath.Join(c.DataDir, "logs", "blittarr.log")
	if c.LogFilePathOrDefault() != want {
		t.Fatalf("want %q got %q", want, c.LogFilePathOrDefault())
	}
}
