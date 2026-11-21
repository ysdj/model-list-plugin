package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testLocations(t *testing.T) locations {
	t.Helper()
	root := t.TempDir()
	return locations{Data: filepath.Join(root, "data"), Config: filepath.Join(root, "data", "config.json"), State: filepath.Join(root, "data", "state.json"), Lock: filepath.Join(root, "data", "sync.lock"), CodexConfig: filepath.Join(root, "codex", "config.toml"), CodexCatalog: filepath.Join(root, "codex", "catalog.json"), ClaudeSettings: filepath.Join(root, "claude", "settings.json")}
}

func TestFirstHookEnablesEachHostAndRespectsDisable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"example","context_window":123456}]}`))
	}))
	defer server.Close()
	root := t.TempDir()
	t.Setenv("MODEL_LIST_PLUGIN_DATA", filepath.Join(root, "data"))
	t.Setenv("MODEL_LIST_PLUGIN_BASE_URL", server.URL)
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	loc, err := locationsFor()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeJSON(loc.State, state{LastUpdateCheckAtByHost: map[string]string{"codex": now, "claude": now}}); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"codex", "claude"} {
		if err := run([]string{"hook", "--host", host}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || !cfg.Targets.Codex || !cfg.Targets.Claude {
		t.Fatalf("targets were not enabled: %+v", cfg)
	}
	if err := run([]string{"disable", "--host", "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"hook", "--host", "codex"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(loc.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Targets.Codex || !cfg.Targets.Claude {
		t.Fatalf("disabled target changed: %+v", cfg)
	}
}

func TestLegacyDisabledConfigurationStaysDisabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MODEL_LIST_PLUGIN_DATA", filepath.Join(root, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	t.Setenv("PLUGIN_DATA", "")
	loc, err := locationsFor()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(loc.Config, []byte(`{"enabled":false,"targets":{"codex":true,"claude":true},"autoUpdate":false}`)); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"codex", "claude"} {
		if err := run([]string{"hook", "--host", host}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil || cfg.Enabled {
		t.Fatalf("legacy disabled configuration changed: %+v %v", cfg, err)
	}
}

func TestFreshConfiguredHostStartsOnFirstHook(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MODEL_LIST_PLUGIN_DATA", filepath.Join(root, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	t.Setenv("PLUGIN_DATA", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"example"}]}`))
	}))
	defer server.Close()
	if err := run([]string{"configure", "--base-url", server.URL, "--auto-update", "false"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"hook", "--host", "codex"}); err != nil {
		t.Fatal(err)
	}
	loc, err := locationsFor()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil || !cfg.Enabled || !cfg.Targets.Codex {
		t.Fatalf("fresh host did not start: %+v %v", cfg, err)
	}
}

func TestFetchPaginationMetadataAndCredentials(t *testing.T) {
	var metadataAuth string
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadataAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"models":[{"id":"model-a","context_window":1048576,"supported_efforts":["high","low"],"default_reasoning_level":"high"}]}`))
	}))
	defer metadata.Close()
	var calls int
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected list request %s", r.URL.Path)
		}
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"image-maker","mode":"image_generation"}],"has_more":true,"last_id":"image-maker"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b","modalities":["text","image"]}],"has_more":false}`))
	}))
	defer listing.Close()
	models, err := fetchCatalog(context.Background(), provider{BaseURL: listing.URL, Kind: "openai", MetadataURL: metadata.URL}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || metadataAuth != "" || len(models) != 2 {
		t.Fatalf("calls=%d metadata auth=%q models=%+v", calls, metadataAuth, models)
	}
	if models[0].ID != "model-a" || models[0].Window != 1048576 || !reflect.DeepEqual(models[0].Efforts, []string{"low", "high"}) || models[0].DefaultEffort != "high" {
		t.Fatalf("incorrect metadata: %+v", models[0])
	}
	if models[1].ID != "model-b" {
		t.Fatalf("vision chat was lost: %+v", models)
	}
}

func TestAnthropicAuthAndRepeatedCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") != "2024-01-01" {
			t.Error("missing Anthropic headers")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model"}],"has_more":true,"last_id":"same"}`))
	}))
	defer server.Close()
	_, err := fetchCatalog(context.Background(), provider{BaseURL: server.URL + "/v1", Kind: "anthropic", AnthropicVersion: "2024-01-01"}, "secret")
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("expected cursor error, got %v", err)
	}
}

func TestCodexPointerIsTopLevelAndRestores(t *testing.T) {
	original := "model = \"model-a\"\n[model_providers.example]\nbase_url = \"http://example.test/v1\"\n"
	updated := setPointer(original, "model_catalog_json = \"/tmp/catalog.json\"")
	pointer, _ := pointerLine(updated)
	if pointer != "/tmp/catalog.json" || strings.Index(updated, "model_catalog_json") > strings.Index(updated, "[model_providers") {
		t.Fatalf("pointer not at top level: %s", updated)
	}
	if actual := setPointer(updated, ""); actual != original {
		t.Fatalf("restore changed config: %q", actual)
	}
}

func TestRawModelIDAndWindows(t *testing.T) {
	models := []model{{ID: "provider/model-a", Label: "Model A", Window: 512000, Efforts: []string{"low", "high"}}, {ID: "other", Label: "Other", Window: 128000}}
	picker, windows := claudeProjection(models, "raw")
	var result struct {
		Options []struct {
			Model     string `json:"model"`
			BehavesAs string `json:"behavesAs"`
		} `json:"options"`
	}
	if err := json.Unmarshal(picker, &result); err != nil {
		t.Fatal(err)
	}
	if result.Options[0].Model != "provider/model-a" || result.Options[0].BehavesAs != "claude-opus-4-8" {
		t.Fatalf("incorrect picker: %s", picker)
	}
	if string(windows["provider/model-a"]) != `{"autoCompactWindow":512000}` {
		t.Fatalf("incorrect window: %s", windows["provider/model-a"])
	}
	catalog := codexCatalog(models, []object{{"slug": "native", "base_instructions": "native", "shell_type": "unified_exec"}})
	entries := catalog["models"].([]object)
	if entries[0]["slug"] != "provider/model-a" || entries[0]["context_window"] != 512000 || entries[0]["shell_type"] != "shell_command" {
		t.Fatalf("incorrect catalog: %+v", entries[0])
	}
}

func TestSyncAndRestorePreservesOtherChanges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"remote-model","context_window":128000}]}`))
	}))
	defer server.Close()
	loc := testLocations(t)
	originalConfig := "model = \"remote-model\"\n[model_providers.test]\nbase_url = \"http://localhost\"\n"
	if err := writeFile(loc.CodexConfig, []byte(originalConfig)); err != nil {
		t.Fatal(err)
	}
	originalSettings := map[string]any{"env": map[string]string{"KEEP": "yes"}, "modelPicker": map[string]any{"options": []any{}}, "modelSettings": map[string]any{"remote-model": map[string]any{"autoCompactWindow": 140000, "other": "before"}}}
	if err := writeJSON(loc.ClaudeSettings, originalSettings); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Enabled = true
	cfg.AutoUpdate = false
	cfg.Provider.BaseURL = server.URL
	saved := state{}
	count, err := syncModels(cfg, loc, &saved, "")
	if err != nil || count != 1 {
		t.Fatalf("sync count=%d error=%v", count, err)
	}
	settings := map[string]any{}
	if err := readJSON(loc.ClaudeSettings, &settings); err != nil {
		t.Fatal(err)
	}
	perModel := settings["modelSettings"].(map[string]any)["remote-model"].(map[string]any)
	perModel["other"] = "changed"
	settings["newSetting"] = true
	if err := writeJSON(loc.ClaudeSettings, settings); err != nil {
		t.Fatal(err)
	}
	if err := disable(cfg, loc, &saved); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(loc.CodexConfig)
	if err != nil || string(restored) != originalConfig {
		t.Fatalf("codex restoration: %q %v", restored, err)
	}
	if _, err := os.Stat(loc.CodexCatalog); !os.IsNotExist(err) {
		t.Fatalf("catalog still exists: %v", err)
	}
	settings = map[string]any{}
	if err := readJSON(loc.ClaudeSettings, &settings); err != nil {
		t.Fatal(err)
	}
	perModel = settings["modelSettings"].(map[string]any)["remote-model"].(map[string]any)
	if perModel["autoCompactWindow"] != float64(140000) || perModel["other"] != "changed" || settings["newSetting"] != true {
		t.Fatalf("lost settings: %+v", settings)
	}
}

func TestSyncRollsBackOnInvalidClaudeSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`)) }))
	defer server.Close()
	loc := testLocations(t)
	if err := writeFile(loc.ClaudeSettings, []byte("{")); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Provider.BaseURL = server.URL
	saved := state{}
	if _, err := syncModels(cfg, loc, &saved, ""); err == nil {
		t.Fatal("expected invalid settings error")
	}
	if _, err := os.Stat(loc.CodexCatalog); !os.IsNotExist(err) {
		t.Fatalf("catalog not rolled back: %v", err)
	}
	raw, _ := os.ReadFile(loc.ClaudeSettings)
	if string(raw) != "{" {
		t.Fatalf("settings changed: %s", raw)
	}
}

func TestZeroIntervalAndSharedLocations(t *testing.T) {
	loc := testLocations(t)
	cfg := defaultConfig()
	cfg.SyncIntervalSeconds = 0
	if err := writeJSON(loc.Config, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(loc.Config)
	if err != nil || loaded.SyncIntervalSeconds != 0 {
		t.Fatalf("zero interval: %+v %v", loaded, err)
	}
	t.Setenv("MODEL_LIST_PLUGIN_DATA", loc.Data)
	t.Setenv("CLAUDE_PLUGIN_DATA", filepath.Join(loc.Data, "host-private"))
	got, err := locationsFor()
	if err != nil || got.Data != loc.Data {
		t.Fatalf("shared root: %+v %v", got, err)
	}
}

func TestAnthropicTokenUsesBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("anthropic-version") == "" || r.Header.Get("x-api-key") != "" {
			t.Errorf("incorrect authentication headers: %+v", r.Header)
		}
		_, _ = w.Write([]byte("{\"data\":[{\"id\":\"model\"}]}"))
	}))
	defer server.Close()
	_, err := fetchCatalog(context.Background(), provider{BaseURL: server.URL, Kind: "anthropic", AuthEnv: "ANTHROPIC_AUTH_TOKEN"}, "token")
	if err != nil {
		t.Fatal(err)
	}
}

func TestHostEnableDisableAndFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MODEL_LIST_PLUGIN_DATA", filepath.Join(root, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("CLAUDE_PLUGIN_DATA", "")
	t.Setenv("PLUGIN_DATA", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{\"data\":[{\"id\":\"example\",\"context_window\":128000}]}"))
	}))
	defer server.Close()
	if err := run([]string{"configure", "--base-url", server.URL, "--auto-update", "false"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"enable", "--host", "codex"}); err != nil {
		t.Fatal(err)
	}
	loc, _ := locationsFor()
	cfg, err := loadConfig(loc.Config)
	if err != nil || !cfg.Enabled || !cfg.Targets.Codex || cfg.Targets.Claude {
		t.Fatalf("codex enable: %+v %v", cfg, err)
	}
	if _, err := os.Stat(loc.ClaudeSettings); !os.IsNotExist(err) {
		t.Fatalf("Claude modified: %v", err)
	}
	if err := run([]string{"enable", "--host", "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"disable", "--host", "codex"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(loc.Config)
	if err != nil || !cfg.Enabled || cfg.Targets.Codex || !cfg.Targets.Claude {
		t.Fatalf("codex disable: %+v %v", cfg, err)
	}
	if _, err := os.Stat(loc.ClaudeSettings); err != nil {
		t.Fatalf("Claude lost: %v", err)
	}
	if err := run([]string{"disable", "--host", "claude"}); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := run([]string{"enable", "--host", "codex"}); err == nil {
		t.Fatal("expected failed fetch")
	}
	cfg, err = loadConfig(loc.Config)
	if err != nil || cfg.Enabled {
		t.Fatalf("failed enable persisted: %+v %v", cfg, err)
	}
}

func TestCodexProviderDiscovery(t *testing.T) {
	loc := testLocations(t)
	t.Setenv("MODEL_LIST_PLUGIN_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("YOUNG_ROUTER_BASE_URL", "")
	t.Setenv("MODEL_LIST_PLUGIN_AUTH_ENV", "")
	t.Setenv("EXAMPLE_CATALOG_KEY", "secret")
	if err := writeFile(loc.CodexConfig, []byte("model_provider = 'example'\n[model_providers.example]\nbase_url = 'https://example.test/v1'\nenv_key = 'EXAMPLE_CATALOG_KEY'\n")); err != nil {
		t.Fatal(err)
	}
	p, key := resolveProvider(defaultConfig(), loc, "claude")
	if p.BaseURL != "https://example.test/v1" || p.AuthEnv != "EXAMPLE_CATALOG_KEY" || key != "secret" {
		t.Fatalf("provider: %+v", p)
	}
}

func TestMigrateHostData(t *testing.T) {
	loc := testLocations(t)
	old := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", old)
	t.Setenv("PLUGIN_DATA", "")
	if err := writeJSON(filepath.Join(old, "config.json"), config{Enabled: true, Targets: targets{Codex: true}, Provider: provider{BaseURL: "https://example.test"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(old, "state.json"), state{ModelIDs: []string{"example"}}); err != nil {
		t.Fatal(err)
	}
	if err := migrateHostData(loc); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil || !cfg.Enabled || cfg.Provider.BaseURL != "https://example.test" {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	saved, err := loadState(loc.State)
	if err != nil || !reflect.DeepEqual(saved.ModelIDs, []string{"example"}) {
		t.Fatalf("state: %+v %v", saved, err)
	}
}
