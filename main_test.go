package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	if err := writeFile(filepath.Join(root, "codex", "config.toml"), []byte("model_provider = 'test'\n[model_providers.test]\nbase_url = '"+server.URL+"'\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(root, "claude", "settings.json"), map[string]any{"env": map[string]string{"ANTHROPIC_BASE_URL": server.URL}}); err != nil {
		t.Fatal(err)
	}
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
	if err := writeFile(filepath.Join(root, "codex", "config.toml"), []byte("model_provider = 'test'\n[model_providers.test]\nbase_url = '"+server.URL+"'\n")); err != nil {
		t.Fatal(err)
	}
	loc, err := locationsFor()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(loc.State, state{LastUpdateCheckAtByHost: map[string]string{"codex": time.Now().UTC().Format(time.RFC3339Nano)}}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"hook", "--host", "codex"}); err != nil {
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
	originalConfig := "model = \"remote-model\"\nmodel_provider = \"test\"\n[model_providers.test]\nbase_url = \"" + server.URL + "\"\n"
	if err := writeFile(loc.CodexConfig, []byte(originalConfig)); err != nil {
		t.Fatal(err)
	}
	originalSettings := map[string]any{"env": map[string]string{"KEEP": "yes", "ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_AUTH_TOKEN": "test-gateway-key"}, "modelPicker": map[string]any{"options": []any{}}, "modelSettings": map[string]any{"remote-model": map[string]any{"autoCompactWindow": 140000, "other": "before"}}}
	if err := writeJSON(loc.ClaudeSettings, originalSettings); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Enabled = true
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
	if err := writeFile(loc.CodexConfig, []byte("model_provider = 'test'\n[model_providers.test]\nbase_url = '"+server.URL+"'\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(loc.ClaudeSettings, []byte("{")); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
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

func TestSyncErrorIsAttributedToItsHost(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"example","context_window":128000}]}`))
	}))
	defer ok.Close()
	loc := testLocations(t)
	if err := writeFile(loc.CodexConfig, []byte("model_provider = 'test'\n[model_providers.test]\nbase_url = '"+ok.URL+"'\n")); err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewServer(http.NotFoundHandler())
	badURL := bad.URL
	bad.Close()
	if err := writeJSON(loc.ClaudeSettings, map[string]any{"env": map[string]string{
		"ANTHROPIC_BASE_URL": badURL, "ANTHROPIC_AUTH_TOKEN": "test-gateway-key",
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.Enabled = true
	saved := state{LastErrorByHost: map[string]string{"codex": "older codex failure"}, LastError: "older codex failure"}
	if _, err := syncModels(cfg, loc, &saved, "claude"); err == nil {
		t.Fatal("expected the unreachable Claude gateway to fail")
	}
	if saved.LastErrorByHost["claude"] == "" {
		t.Fatalf("Claude's failure was not recorded: %+v", saved)
	}
	if saved.LastErrorByHost["codex"] != "older codex failure" || saved.LastError != saved.LastErrorByHost["claude"] {
		t.Fatalf("the failure leaked across hosts: %+v", saved)
	}

	// The next successful Claude sync clears only Claude's error; Codex's
	// remains a fact about that host until Codex syncs.
	if err := writeJSON(loc.ClaudeSettings, map[string]any{"env": map[string]string{
		"ANTHROPIC_BASE_URL": ok.URL, "ANTHROPIC_AUTH_TOKEN": "test-gateway-key",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncModels(cfg, loc, &saved, "claude"); err != nil {
		t.Fatal(err)
	}
	if _, exists := saved.LastErrorByHost["claude"]; exists {
		t.Fatalf("Claude's error survived its success: %+v", saved)
	}
	if saved.LastErrorByHost["codex"] != "older codex failure" || saved.LastError != "older codex failure" {
		t.Fatalf("Codex's error was cleared by Claude's sync: %+v", saved)
	}
}

func TestStatusPublishesEachHostsError(t *testing.T) {
	cfg := defaultConfig()
	cfg.Enabled = true
	loc := testLocations(t)
	saved := state{
		LastError:       "codex failed",
		LastErrorByHost: map[string]string{"codex": "codex failed"},
	}
	out := status(cfg, loc, saved, "claude")
	errors, ok := out["lastErrorByHost"].(map[string]string)
	if !ok || errors["codex"] != "codex failed" {
		t.Fatalf("status lost the per-host error: %+v", out["lastErrorByHost"])
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
	if err := writeFile(filepath.Join(root, "codex", "config.toml"), []byte("model_provider = 'test'\n[model_providers.test]\nbase_url = '"+server.URL+"'\n")); err != nil {
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
	// A gateway named only by URL has no credential for the hook to present,
	// so the test supplies the key the same way an operator would.
	if err := writeJSON(filepath.Join(root, "claude", "settings.json"), map[string]any{"env": map[string]string{"ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_AUTH_TOKEN": "test-gateway-key"}}); err != nil {
		t.Fatal(err)
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
	p, key, err := resolveProvider(loc, "codex")
	if err != nil || p.BaseURL != "https://example.test/v1" || p.AuthEnv != "EXAMPLE_CATALOG_KEY" || key != "secret" {
		t.Fatalf("provider: %+v, error: %v", p, err)
	}
}

func TestClientSourcesIgnorePluginOverrides(t *testing.T) {
	loc := testLocations(t)
	t.Setenv("MODEL_LIST_PLUGIN_BASE_URL", "https://obsolete.example.test")
	t.Setenv("ANTHROPIC_BASE_URL", "https://old-session.example.test")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "fixture-old-session-key")
	if err := writeFile(loc.CodexConfig, []byte(`model_provider = 'selected'
[model_providers.other]
base_url = 'https://other.example.test/v1'
[model_providers.selected]
base_url = 'https://codex.example.test/v1'
model_catalog_url = 'https://codex.example.test/catalog'
experimental_bearer_token = 'fixture-codex-key'
`)); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(loc.ClaudeSettings, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://claude.example.test","ANTHROPIC_API_KEY":"fixture-claude-key"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(loc.Config, []byte(`{"enabled":true,"provider":{"baseUrl":"https://obsolete.example.test"}}`)); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ host, base, catalog, mode, key string }{
		{"codex", "https://codex.example.test/v1", "https://codex.example.test/catalog", "bearer", "fixture-codex-key"},
		{"claude", "https://claude.example.test", "", "x-api-key", "fixture-claude-key"},
	} {
		p, key, err := resolveProvider(loc, test.host)
		if err != nil || p.BaseURL != test.base || p.MetadataURL != test.catalog || p.authMode() != test.mode || key != test.key {
			t.Fatalf("%s did not follow its selected provider: %+v, %v", test.host, p, err)
		}
	}
	output, err := json.Marshal(status(cfg, loc, state{}, ""))
	if err != nil || strings.Contains(string(output), "obsolete.example.test") || strings.Contains(string(output), "fixture-") {
		t.Fatal("status includes a retired provider or a credential")
	}
	if err := writeFile(loc.ClaudeSettings, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://claude.example.test"}}`)); err != nil {
		t.Fatal(err)
		// The endpoint changed, so the old session's key must not follow it.  The
		// resolver now reports that it has nothing to present instead of returning
		// an empty key that turns into a bare 401 later on.
		if _, key, err := resolveProvider(loc, "claude"); key != "" || err == nil {
			t.Fatalf("the new Claude endpoint should report that it has no key, got %q, %v", key, err)
		}
	}
}

func TestCatalogOnlyProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog" {
			_, _ = w.Write([]byte(`{"models":[{"slug":"catalog-model","context_window":120000}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	models, err := fetchCatalog(context.Background(), provider{BaseURL: server.URL, MetadataURL: server.URL + "/catalog"}, "")
	if err != nil || len(models) != 1 || models[0].ID != "catalog-model" || models[0].Window != 120000 {
		t.Fatalf("catalog model: %+v, %v", models, err)
	}
}

func TestPublishedContextWinsAndMissingContextUsesPublicCatalogs(t *testing.T) {
	parse := func(data string) any {
		t.Helper()
		var value any
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	listing, _ := rows(parse(`{"data":[{"id":"public-name","context_window":48000,"max_context_window":48000,"effective_context_window_percent":95},{"id":"bedrock.example@stable"},{"id":"listed-without-context"}]}`))
	metadata, _ := rows(parse(`{"models":[{"slug":"public-name","context_window":200000,"max_context_window":250000,"default_reasoning_effort":"high","supported_reasoning_levels":["low","high"]}]}`))
	cache := publicCache{Catalogs: [len(publicSources)]map[string]model{
		publicRecords(parse(`{"a":{"example":{"id":"example","contextWindow":100000}},"b":{"example":{"id":"example","contextWindow":95000}}}`), publicSources[0]),
		publicRecords(parse(`{"models":[{"slug":"public-name","context_window":200000},{"slug":"listed-without-context","context_window":190000,"max_context_window":300000}]}`), publicSources[1]),
		publicRecords(parse(`{"vendor/example":{"limit":{"context":80000,"input":160000}}}`), publicSources[2]),
	}}
	models := fillPublicContexts(normalize(listing, metadata), cache)
	if len(models) != 3 || models[0].Window != 48000 || models[0].MaxWindow != 48000 || models[0].DefaultEffort != "high" {
		t.Fatalf("API window lost while merging metadata: %+v", models)
	}
	if models[1].Window != 95000 || models[2].Window != 190000 || models[2].MaxWindow != 300000 {
		t.Fatalf("public context did not follow catalog precedence: %+v", models)
	}
}

func TestMigrateHostData(t *testing.T) {
	loc := testLocations(t)
	old := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", old)
	t.Setenv("PLUGIN_DATA", "")
	if err := writeFile(filepath.Join(old, "config.json"), []byte(`{"enabled":true,"targets":{"codex":true},"provider":{"baseUrl":"https://example.test"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(old, "state.json"), state{ModelIDs: []string{"example"}}); err != nil {
		t.Fatal(err)
	}
	if err := migrateHostData(loc); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil || !cfg.Enabled {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	saved, err := loadState(loc.State)
	if err != nil || !reflect.DeepEqual(saved.ModelIDs, []string{"example"}) {
		t.Fatalf("state: %+v %v", saved, err)
	}
}

func TestClaudeListingFallsBackToTheSessionKey(t *testing.T) {
	// A settings file can name a gateway key that the app has since replaced.
	// The session that runs this hook carries the live one; the listing must
	// present it instead of repeating the stale copy's 401.
	loc := testLocations(t)
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := first(r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		seen = append(seen, key)
		if key != "live-gateway-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a","max_input_tokens":500000}]}`))
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "live-gateway-key")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := writeJSON(loc.ClaudeSettings, map[string]any{"env": map[string]string{
		"ANTHROPIC_BASE_URL":   server.URL,
		"ANTHROPIC_AUTH_TOKEN": "stale-gateway-key",
	}}); err != nil {
		t.Fatal(err)
	}
	p, key, err := resolveProvider(loc, "claude")
	if err != nil || key != "stale-gateway-key" {
		t.Fatalf("configured key should stay first: %q %v", key, err)
	}
	if len(p.Credentials) != 2 || p.Credentials[1] != (credential{"live-gateway-key", "anthropic-token"}) {
		t.Fatalf("session credential was not offered: %+v", p.Credentials)
	}
	models, err := fetchCatalog(context.Background(), p, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "model-a" || models[0].Window != 500000 {
		t.Fatalf("wrong catalog: %+v", models)
	}
	if !reflect.DeepEqual(seen, []string{"stale-gateway-key", "live-gateway-key"}) {
		t.Fatalf("credentials tried: %v", seen)
	}
}

func TestClaudeSessionKeySurvivesAMissingV1Suffix(t *testing.T) {
	// Claude Desktop's session exports the bare host while settings.json names
	// the same host with a /v1 suffix.  Those are one endpoint, so the live key
	// must still be offered when the stored one is rejected.
	loc := testLocations(t)
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := first(r.Header.Get("x-api-key"), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		seen = append(seen, key)
		if key != "live-gateway-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a","max_input_tokens":500000}]}`))
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	t.Setenv("ANTHROPIC_API_KEY", "live-gateway-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	if err := writeJSON(loc.ClaudeSettings, map[string]any{"env": map[string]string{
		"ANTHROPIC_BASE_URL":   server.URL + "/v1/",
		"ANTHROPIC_AUTH_TOKEN": "stale-gateway-key",
	}}); err != nil {
		t.Fatal(err)
	}
	p, key, err := resolveProvider(loc, "claude")
	if err != nil || key != "stale-gateway-key" {
		t.Fatalf("configured key should stay first: %q %v", key, err)
	}
	if !strings.HasSuffix(p.BaseURL, "/v1/") {
		t.Fatalf("the catalog base must keep the suffix the settings name: %q", p.BaseURL)
	}
	if len(p.Credentials) != 2 || p.Credentials[1] != (credential{"live-gateway-key", "x-api-key"}) {
		t.Fatalf("session credential was dropped over a /v1 suffix: %+v", p.Credentials)
	}
	if _, err := fetchCatalog(context.Background(), p, key); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"stale-gateway-key", "live-gateway-key"}) {
		t.Fatalf("credentials tried: %v", seen)
	}
}

func TestClaudeSessionKeyNeverTravelsToAnotherGateway(t *testing.T) {
	loc := testLocations(t)
	t.Setenv("ANTHROPIC_BASE_URL", "https://other.example.test")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "other-gateway-key")
	if err := writeJSON(loc.ClaudeSettings, map[string]any{"env": map[string]string{
		"ANTHROPIC_BASE_URL": "https://this.example.test",
	}}); err != nil {
		t.Fatal(err)
	}
	p, key, err := resolveProvider(loc, "claude")
	if key != "" || len(p.Credentials) != 0 {
		t.Fatalf("another gateway's key was adopted: %+v %q %v", p.Credentials, key, err)
	}
	// Nothing to present is now reported, so the sync fails with a message that
	// names the fix instead of a bare 401 from the listing call.
	if err == nil || !strings.Contains(err.Error(), "no gateway key") {
		t.Fatalf("an empty credential should be reported, got %v", err)
	}
}

func TestCodexListingFallsBackToAuthJSON(t *testing.T) {
	loc := testLocations(t)
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		if key != "auth-json-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a","context_window":500000}]}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "stale-env-key")
	if err := writeFile(loc.CodexConfig, []byte("model_provider = 'custom'\n[model_providers.custom]\nbase_url = '"+server.URL+"/v1'\nrequires_openai_auth = true\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(filepath.Dir(loc.CodexConfig), "auth.json"), map[string]any{"OPENAI_API_KEY": "auth-json-key"}); err != nil {
		t.Fatal(err)
	}
	p, key, err := resolveProvider(loc, "codex")
	if err != nil || key != "stale-env-key" {
		t.Fatalf("environment key should stay first: %q %v", key, err)
	}
	if len(p.Credentials) != 2 || p.Credentials[1].Value != "auth-json-key" {
		t.Fatalf("auth.json was not offered: %+v", p.Credentials)
	}
	if _, err := fetchCatalog(context.Background(), p, key); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"stale-env-key", "auth-json-key"}) {
		t.Fatalf("credentials tried: %v", seen)
	}
}

func TestUpdateDueChecksAtEveryStartup(t *testing.T) {
	just := time.Now().UTC().Format(time.RFC3339Nano)
	if updateDue(just, false) {
		t.Fatal("a recent check must not repeat on a prompt")
	}
	if !updateDue(just, true) {
		t.Fatal("a session start must check even after a recent check")
	}
	if !updateDue("", false) || !updateDue("not a timestamp", false) {
		t.Fatal("an unknown last check is due")
	}
}

func TestStartupOptionIsAccepted(t *testing.T) {
	command, options, err := parseArgs([]string{"hook", "--host", "claude", "--startup"})
	if err != nil || command != "hook" || options["startup"] != "true" {
		t.Fatalf("startup option not parsed: %q %v %v", command, options, err)
	}
}

func TestDesktopBundleIsNamedForThisPlatform(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	versionDir := filepath.Join(root, "Claude-3p", "claude-code", "2.1.330", "hash")
	bundle := filepath.Join(versionDir, "claude.exe")
	if runtime.GOOS != "windows" {
		versionDir = filepath.Join(root, "Library", "Application Support", "Claude-3p", "claude-code", "2.1.330", "hash")
		bundle = filepath.Join(versionDir, "claude.app", "Contents", "MacOS", "claude")
	}
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := desktopHostExecutables("claude")
	for _, path := range found {
		if path == bundle {
			return
		}
	}
	t.Fatalf("bundle %q not named in %v", bundle, found)
}

// TestClaudeBehaviorCoversStatedWindow pins the regression behind a gateway
// model showing 200K.  Claude Code resolves a session's window as
// min(assumedWindow, autoCompactWindow), so a row that borrows a behavior
// assuming less than the provider states silently truncates it.
func TestClaudeBehaviorCoversStatedWindow(t *testing.T) {
	cases := []struct {
		stated int
		want   string
	}{
		// The window considered is the one the projection states, so a claim
		// above the ceiling is matched at the ceiling.
		{200000, "claude-sonnet-4-6"},
		{262144, "claude-opus-4-8"},
		{500000, "claude-opus-4-8"},
		{1000000, "claude-opus-5-5"},
		{1048576, "claude-opus-5-5"},
		{0, "claude-sonnet-4-6"},
	}
	for _, c := range cases {
		window, _ := claudeCompactWindow(c.stated)
		if got := claudeBehavior(window); got != c.want {
			t.Errorf("claudeBehavior(%d) = %q, want %q", c.stated, got, c.want)
		}
	}
	// Whatever a row borrows must be able to carry the window the projection
	// states, which is what the client compares against.
	for _, c := range cases {
		window, stated := claudeCompactWindow(c.stated)
		if !stated {
			continue
		}
		behavior := claudeBehavior(window)
		assumed := map[string]int{
			"claude-sonnet-4-6": 200000,
			"claude-opus-4-8":   1000000,
			"claude-opus-5-5":   1000000,
		}[behavior]
		if assumed < window {
			t.Errorf("behavior %q assumes %d, below the projected %d", behavior, assumed, window)
		}
	}
}

// TestClaudeProjectionWritesEveryOfferedModel guards the case that produced a
// 200K readout for a model the provider describes without a context window:
// the row must still be offered, and it must still get an entry so a stale one
// cannot survive and an absent one cannot fall back to 200000.
func TestClaudeProjectionWritesEveryOfferedModel(t *testing.T) {
	models := []model{
		{ID: "windowed", Label: "Windowed", Window: 500000},
		{ID: "unwindowed", Label: "Unwindowed"},
		{ID: "tiny", Label: "Tiny", Window: 4096},
	}
	picker, windows := claudeProjection(models, "raw")

	var parsed struct {
		Options []struct {
			Model     string `json:"model"`
			BehavesAs string `json:"behavesAs"`
		} `json:"options"`
	}
	if err := json.Unmarshal(picker, &parsed); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]string, len(parsed.Options))
	for _, option := range parsed.Options {
		seen[option.Model] = option.BehavesAs
	}
	for _, id := range []string{"windowed", "unwindowed", "tiny"} {
		if _, ok := seen[id]; !ok {
			t.Errorf("model %q was not offered", id)
		}
	}
	if len(windows) != len(models) {
		t.Fatalf("expected an entry per model, got %d", len(windows))
	}
	if got := string(windows["windowed"]); got != `{"autoCompactWindow":500000}` {
		t.Errorf("windowed entry = %s", got)
	}
	if got := string(windows["unwindowed"]); got != `{}` {
		t.Errorf("a model without a stated window should carry no window, got %s", got)
	}
	if got := string(windows["tiny"]); got != `{"autoCompactWindow":100000}` {
		t.Errorf("a window below the floor should clamp up, got %s", got)
	}
	for _, option := range parsed.Options {
		if option.Model == "windowed" && option.BehavesAs == "claude-sonnet-4-6" {
			t.Errorf("a %d-token model must not borrow the 200K behavior", 500000)
		}
	}
}

// TestClaudeProjectionWritesOneRowPerModel guards the picker shape the
// handoff from Young Router depends on: one visible row per model, not one per
// legacy spelling.  The old projection offered every historical alias as a row
// (four rows per model once the "[1m]" and hashed spellings were included),
// which made the plugin's list impossible to reconcile with the app's own
// thirteen-row list.
func TestClaudeProjectionWritesOneRowPerModel(t *testing.T) {
	models := []model{{ID: "gpt-6.2-sol", Label: "gpt-6.2-sol", Window: 500000}}
	picker, windows := claudeProjection(models, "derived")

	hash := sha256.Sum256([]byte("gpt-6.2-sol"))
	legacy := "claude-mythos-" + hex.EncodeToString(hash[:])[:6]

	var parsed struct {
		Options []struct {
			Model     string `json:"model"`
			BehavesAs string `json:"behavesAs"`
		} `json:"options"`
	}
	if err := json.Unmarshal(picker, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Options) != 1 {
		t.Fatalf("expected one row per model, got %d", len(parsed.Options))
	}
	if parsed.Options[0].Model != legacy || parsed.Options[0].BehavesAs == "" {
		t.Errorf("row = %+v, want one behaving row for %q", parsed.Options[0], legacy)
	}
	if got := string(windows[legacy]); got != `{"autoCompactWindow":500000}` {
		t.Errorf("primary window entry = %s", got)
	}
}

// TestClaudeProjectionStatesBothSpellingsOfAOneMillionModel covers the id the
// picker names versus the id a session runs.  The row carries "[1m]", but the
// session writes the suffix-free id; a number under only one spelling leaves
// the other on Claude Code's 200K default.
func TestClaudeProjectionStatesBothSpellingsOfAOneMillionModel(t *testing.T) {
	models := []model{{ID: "deepseek-v4.1-flash", Label: "deepseek-v4.1-flash", Window: 1000000}}
	picker, windows := claudeProjection(models, "derived")
	hash := sha256.Sum256([]byte("deepseek-v4.1-flash"))
	base := "claude-mythos-" + hex.EncodeToString(hash[:])[:6]
	for _, id := range []string{base, base + "[1m]"} {
		if got := string(windows[id]); got != `{"autoCompactWindow":1000000}` {
			t.Errorf("window under %q = %s", id, got)
		}
	}
	var parsed struct {
		Options []struct {
			Model string `json:"model"`
		} `json:"options"`
	}
	if err := json.Unmarshal(picker, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Options) != 1 || parsed.Options[0].Model != base+"[1m]" {
		t.Errorf("picker options = %+v", parsed.Options)
	}
}

// TestClaudeProjectionUsesProviderMetadata pins the handoff contract: when the
// provider states claude_alias and behaves_as, the projection uses them
// directly instead of re-deriving the Desktop id or the behavior tier.
func TestClaudeProjectionUsesProviderMetadata(t *testing.T) {
	models := []model{{
		ID:          "deepseek-v4.1-flash",
		Label:       "deepseek-v4.1-flash",
		ClaudeAlias: "claude-mythos-eb1394[1m]",
		BehavesAs:   "claude-opus-5-5",
		Window:      1000000,
	}}
	picker, windows := claudeProjection(models, "metadata")
	var parsed struct {
		Options []struct {
			Model       string `json:"model"`
			Label       string `json:"label"`
			Description string `json:"description"`
			BehavesAs   string `json:"behavesAs"`
		} `json:"options"`
	}
	if err := json.Unmarshal(picker, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Options) != 1 {
		t.Fatalf("expected one row, got %+v", parsed.Options)
	}
	row := parsed.Options[0]
	if row.Model != "claude-mythos-eb1394[1m]" || row.Label != "deepseek-v4.1-flash" ||
		row.BehavesAs != "claude-opus-5-5" || row.Description != "\u200b" {
		t.Errorf("row = %+v", row)
	}
	for _, id := range []string{"claude-mythos-eb1394", "claude-mythos-eb1394[1m]"} {
		if got := string(windows[id]); got != `{"autoCompactWindow":1000000}` {
			t.Errorf("window under %q = %s", id, got)
		}
	}
}

// TestClaudeProjectionLeavesRealClaudeRowsToTheClient keeps a row whose id the
// client already knows free of this projection's label, subtitle and behavior.
// The app's own picker writes those rows as just their model id; inventing a
// label or a substitute would hide the catalogue's own name and ladder.
func TestClaudeProjectionLeavesRealClaudeRowsToTheClient(t *testing.T) {
	models := []model{{ID: "claude-fable-5-2", Label: "claude-fable-5-2", Window: 1000000}}
	picker, _ := claudeProjection(models, "raw")
	var parsed struct {
		Options []map[string]any `json:"options"`
	}
	if err := json.Unmarshal(picker, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Options) != 1 {
		t.Fatalf("expected one row, got %+v", parsed.Options)
	}
	if len(parsed.Options[0]) != 1 || parsed.Options[0]["model"] != "claude-fable-5-2" {
		t.Errorf("row = %+v, want only its model id", parsed.Options[0])
	}
}

// TestNormalizeDerivesWindowFromOutputCap covers endpoints that publish a model
// with only an output cap.  The window has to be derived from it, because a
// zero window is what sends the client back to its 200000 fallback.
func TestNormalizeDerivesWindowFromOutputCap(t *testing.T) {
	rows := []modelRow{
		{id: "cap-only", fields: object{"max_tokens": float64(32000)}},
		{id: "both", fields: object{"max_tokens": float64(32000), "max_input_tokens": float64(128000)}},
		{id: "none", fields: object{}},
	}
	got := normalize(rows, nil)
	byID := make(map[string]int, len(got))
	for _, m := range got {
		byID[m.ID] = m.Window
	}
	if byID["cap-only"] != 128000 {
		t.Errorf("cap-only window = %d, want 128000", byID["cap-only"])
	}
	if byID["both"] != 128000 {
		t.Errorf("a stated window must win over a derived one, got %d", byID["both"])
	}
	if byID["none"] != 0 {
		t.Errorf("a model with no numbers should stay at 0, got %d", byID["none"])
	}
}
