package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportDirectoryPrecedence(t *testing.T) {
	t.Setenv("SPXQ_REPORT_DIR", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := defaultReportDir(); got != "." {
		t.Fatalf("default: %q", got)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(config, "spxq"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(config, "spxq", "report-dir"), []byte("/configured/reports\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := defaultReportDir(); got != "/configured/reports" {
		t.Fatalf("config: %q", got)
	}
	t.Setenv("SPXQ_REPORT_DIR", "/environment/reports")
	if got := defaultReportDir(); got != "/environment/reports" {
		t.Fatalf("environment: %q", got)
	}
	var out, stderr bytes.Buffer
	// An explicit directory must override both the environment and local config.
	if err = run(context.Background(), []string{"reports", "--dir", t.TempDir()}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "REPORT KEY") {
		t.Fatal(out.String())
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out, stderr bytes.Buffer
		if err := run(context.Background(), []string{arg}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "spxq "+version+" (") {
			t.Fatal(out.String())
		}
	}
}

func TestReportDuration(t *testing.T) {
	dir := t.TempDir()
	// Despite its name, SPX's wall_time_ms metadata is in microseconds.
	meta := `{"enabled_metrics":["wt"],"wall_time_ms":1349258291}`
	if err := os.WriteFile(filepath.Join(dir, "sample.json"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := run(context.Background(), []string{"reports", "--dir", dir}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1349.258s") {
		t.Fatalf("incorrect report duration: %s", out.String())
	}
}
