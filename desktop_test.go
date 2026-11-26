package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDesktopConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDesktopGatewayUsesTheAppliedConfiguration(t *testing.T) {
	dir := t.TempDir()
	writeDesktopConfig(t, dir, "_meta.json", `{"appliedId":"e192a3ee","entries":[]}`)
	writeDesktopConfig(t, dir, "e192a3ee.json", `{"inferenceGatewayBaseUrl":"https://lv.example.com","inferenceGatewayApiKey":"plain-key","inferenceGatewayAuthScheme":"bearer","inferenceProvider":"gateway"}`)
	writeDesktopConfig(t, dir, "older.json", `{"inferenceGatewayBaseUrl":"https://old.example.com"}`)

	got, ok := desktopGatewayFrom([]string{dir})
	if !ok {
		t.Fatal("no gateway found")
	}
	if got.BaseURL != "https://lv.example.com" || got.APIKey != "plain-key" || got.AuthScheme != "bearer" || got.SealedKey {
		t.Fatalf("unexpected configuration: %+v", got)
	}
}

func TestDesktopGatewayNeverUsesASealedKey(t *testing.T) {
	dir := t.TempDir()
	writeDesktopConfig(t, dir, "_meta.json", `{"appliedId":"cfg"}`)
	writeDesktopConfig(t, dir, "cfg.json", `{"inferenceGatewayBaseUrl":"https://lv.example.com","inferenceGatewayApiKey":"__sealed__:v1:abcdef"}`)

	got, ok := desktopGatewayFrom([]string{dir})
	if !ok || got.BaseURL != "https://lv.example.com" {
		t.Fatalf("gateway not found: %+v %v", got, ok)
	}
	if got.APIKey != "" || !got.SealedKey {
		t.Fatalf("a sealed key must stay unread: %+v", got)
	}
}

func TestDesktopGatewayFallsBackToTheNewestConfiguration(t *testing.T) {
	dir := t.TempDir()
	old := writeDesktopConfig(t, dir, "old.json", `{"inferenceGatewayBaseUrl":"https://old.example.com"}`)
	fresh := writeDesktopConfig(t, dir, "fresh.json", `{"inferenceGatewayBaseUrl":"https://fresh.example.com"}`)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	got, ok := desktopGatewayFrom([]string{dir})
	if !ok || got.BaseURL != "https://fresh.example.com" {
		t.Fatalf("expected the newest configuration, got %+v", got)
	}
}

func TestDesktopGatewaySkipsDirectoriesWithoutAGateway(t *testing.T) {
	dir := t.TempDir()
	writeDesktopConfig(t, dir, "_meta.json", `{"appliedId":"cfg"}`)
	writeDesktopConfig(t, dir, "cfg.json", `{"inferenceProvider":"gateway"}`)

	if _, ok := desktopGatewayFrom([]string{filepath.Join(dir, "missing"), dir}); ok {
		t.Fatal("a configuration without a gateway URL must not be reported")
	}
}

func TestDesktopConfigDirsFollowTheOperatingSystem(t *testing.T) {
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "Local"))
	dirs := desktopConfigDirs()
	if len(dirs) == 0 {
		t.Fatal("no configuration directories")
	}
	for _, dir := range dirs {
		if !strings.HasSuffix(filepath.ToSlash(dir), "/Claude-3p/configLibrary") {
			t.Fatalf("unexpected directory %q", dir)
		}
	}
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		want := filepath.Join(configDir, "Claude-3p", "configLibrary")
		found := false
		for _, dir := range dirs {
			if dir == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("the platform configuration directory %q is missing from %v", want, dirs)
		}
	}
}
