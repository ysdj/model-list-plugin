package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const version = "0.2.0"
const pluginName = "model-list-plugin"

type targets struct {
	Codex  bool `json:"codex"`
	Claude bool `json:"claude"`
}

type config struct {
	Enabled             bool            `json:"enabled"`
	Targets             targets         `json:"targets"`
	InitializedTargets  map[string]bool `json:"initializedTargets"`
	Provider            provider        `json:"provider"`
	ClaudeAliasMode     string          `json:"claudeAliasMode"`
	SyncIntervalSeconds int             `json:"syncIntervalSeconds"`
	AutoUpdate          bool            `json:"autoUpdate"`
	UpdateCheckHours    int             `json:"updateCheckHours"`
}

func defaultConfig() config {
	return config{Targets: targets{true, true}, InitializedTargets: map[string]bool{}, Provider: provider{Kind: "auto", AnthropicVersion: "2023-06-01"}, ClaudeAliasMode: "raw", SyncIntervalSeconds: 60, AutoUpdate: true, UpdateCheckHours: 24}
}

type codexState struct {
	OriginalPointer string `json:"originalPointer,omitempty"`
	OriginalLine    string `json:"originalLine,omitempty"`
	HadConfig       bool   `json:"hadConfig"`
	ManagedPath     string `json:"managedPath"`
}

type managedState struct {
	Codex              *codexState                `json:"codex,omitempty"`
	ClaudeSnapshot     map[string]json.RawMessage `json:"claudeSnapshot,omitempty"`
	ClaudeAliases      []string                   `json:"claudeAliases,omitempty"`
	ClaudeLastPicker   json.RawMessage            `json:"claudeLastPicker,omitempty"`
	ClaudeLastSettings map[string]json.RawMessage `json:"claudeLastSettings,omitempty"`
	ClaudeHadSettings  *bool                      `json:"claudeHadSettings,omitempty"`
}

type state struct {
	Version                 string            `json:"version"`
	Managed                 managedState      `json:"managed"`
	LastSyncAt              string            `json:"lastSyncAt,omitempty"`
	LastSyncAtByTarget      map[string]string `json:"lastSyncAtByTarget,omitempty"`
	LastUpdateCheckAtByHost map[string]string `json:"lastUpdateCheckAtByHost,omitempty"`
	LastUpdateErrorByHost   map[string]string `json:"lastUpdateErrorByHost,omitempty"`
	LastError               string            `json:"lastError,omitempty"`
	ModelIDs                []string          `json:"modelIds,omitempty"`
}

type locations struct {
	Data, Config, State, Lock, CodexConfig, CodexCatalog, ClaudeSettings string
}

func locationsFor() (locations, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return locations{}, err
	}
	data := os.Getenv("MODEL_LIST_PLUGIN_DATA")
	if data == "" {
		switch runtime.GOOS {
		case "darwin":
			data = filepath.Join(home, "Library", "Application Support", pluginName)
		case "windows":
			data = filepath.Join(first(os.Getenv("APPDATA"), home), pluginName)
		default:
			data = filepath.Join(first(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), pluginName)
		}
	}
	codexHome := first(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	claudeHome := first(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
	return locations{
		Data:           data,
		Config:         first(os.Getenv("MODEL_LIST_PLUGIN_CONFIG"), filepath.Join(data, "config.json")),
		State:          filepath.Join(data, "state.json"),
		Lock:           filepath.Join(data, "sync.lock"),
		CodexConfig:    filepath.Join(codexHome, "config.toml"),
		CodexCatalog:   filepath.Join(codexHome, pluginName+"-catalog.json"),
		ClaudeSettings: first(os.Getenv("MODEL_LIST_PLUGIN_CLAUDE_SETTINGS"), filepath.Join(claudeHome, "settings.json")),
	}, nil
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("invalid JSON in %s: %w", path, err)
	}
	return nil
}

func loadConfig(path string) (config, error) {
	value := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return value, fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &value); err != nil {
			return value, fmt.Errorf("invalid JSON in %s: %w", path, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return value, fmt.Errorf("invalid JSON in %s: %w", path, err)
		}
		if _, exists := fields["initializedTargets"]; !exists {
			value.InitializedTargets = map[string]bool{"codex": true, "claude": true}
		}
	}
	if value.SyncIntervalSeconds < 0 {
		return value, errors.New("syncIntervalSeconds must be non-negative")
	}
	if value.UpdateCheckHours < 1 {
		value.UpdateCheckHours = 24
	}
	if value.ClaudeAliasMode == "" {
		value.ClaudeAliasMode = "raw"
	}
	return value, nil
}

func migrateHostData(loc locations) error {
	if _, err := os.Stat(loc.Config); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, root := range []string{os.Getenv("CLAUDE_PLUGIN_DATA"), os.Getenv("PLUGIN_DATA")} {
		if root == "" || filepath.Clean(root) == filepath.Clean(loc.Data) {
			continue
		}
		oldConfig := filepath.Join(root, "config.json")
		data, err := os.ReadFile(oldConfig)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var cfg config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("invalid JSON in %s: %w", oldConfig, err)
		}
		oldState := filepath.Join(root, "state.json")
		stateData, err := os.ReadFile(oldState)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(stateData) > 0 {
			var saved state
			if err := json.Unmarshal(stateData, &saved); err != nil {
				return fmt.Errorf("invalid JSON in %s: %w", oldState, err)
			}
			if err := writeFile(loc.State, stateData); err != nil {
				return err
			}
		}
		return writeFile(loc.Config, data)
	}
	return nil
}

func loadState(path string) (state, error) {
	value := state{Version: version}
	err := readJSON(path, &value)
	return value, err
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".model-list-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

func removeFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// A single lock serializes both host hooks and manual commands, including state reads.
func acquireLock(path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	until := time.Now().Add(wait)
	for {
		err := os.Mkdir(path, 0700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Minute {
			if info.IsDir() {
				_ = os.Remove(path)
			} else {
				_ = removeFile(path)
			}
			continue
		}
		if time.Now().After(until) {
			return nil, errors.New("another model-list-plugin operation is running")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func settingsEnv(path, name string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if readJSON(path, &settings) == nil {
		return strings.TrimSpace(settings.Env[name])
	}
	return ""
}

func codexProvider(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var document struct {
		ModelProvider  string `toml:"model_provider"`
		OpenAIBaseURL  string `toml:"openai_base_url"`
		ModelProviders map[string]struct {
			BaseURL string `toml:"base_url"`
			EnvKey  string `toml:"env_key"`
		} `toml:"model_providers"`
	}
	if toml.Unmarshal(data, &document) != nil {
		return "", ""
	}
	selected := document.ModelProviders[document.ModelProvider]
	return first(selected.BaseURL, document.OpenAIBaseURL), selected.EnvKey
}

func resolveProvider(cfg config, loc locations, host string) (provider, string) {
	p := cfg.Provider
	anthropicKey := first(settingsEnv(loc.ClaudeSettings, "ANTHROPIC_AUTH_TOKEN"), settingsEnv(loc.ClaudeSettings, "ANTHROPIC_API_KEY"))
	openAIKey := settingsEnv(loc.ClaudeSettings, "OPENAI_API_KEY")
	p.BaseURL = first(p.BaseURL, os.Getenv("MODEL_LIST_PLUGIN_BASE_URL"))
	if p.BaseURL == "" && host == "codex" {
		base, envKey := codexProvider(loc.CodexConfig)
		p.BaseURL = base
		p.AuthEnv = first(p.AuthEnv, envKey)
	}
	if p.BaseURL == "" && p.Kind == "anthropic" {
		p.BaseURL = settingsEnv(loc.ClaudeSettings, "ANTHROPIC_BASE_URL")
	}
	p.BaseURL = first(p.BaseURL, settingsEnv(loc.ClaudeSettings, "OPENAI_BASE_URL"), settingsEnv(loc.ClaudeSettings, "ANTHROPIC_BASE_URL"), os.Getenv("YOUNG_ROUTER_BASE_URL"))
	p.MetadataURL = first(p.MetadataURL, os.Getenv("MODEL_LIST_PLUGIN_METADATA_URL"))
	p.AuthEnv = first(p.AuthEnv, os.Getenv("MODEL_LIST_PLUGIN_AUTH_ENV"))
	if p.BaseURL == "" && p.Kind != "anthropic" {
		base, envKey := codexProvider(loc.CodexConfig)
		p.BaseURL = base
		p.AuthEnv = first(p.AuthEnv, envKey)
	}
	if p.AuthEnv == "" {
		if host != "codex" && (p.Kind == "anthropic" || (p.Kind == "auto" && anthropicKey != "" && openAIKey == "")) {
			if settingsEnv(loc.ClaudeSettings, "ANTHROPIC_AUTH_TOKEN") != "" {
				p.AuthEnv = "ANTHROPIC_AUTH_TOKEN"
			} else {
				p.AuthEnv = "ANTHROPIC_API_KEY"
			}
		} else {
			p.AuthEnv = "OPENAI_API_KEY"
		}
	}
	key := settingsEnv(loc.ClaudeSettings, p.AuthEnv)
	if host == "codex" && os.Getenv(p.AuthEnv) == "" {
		var auth map[string]any
		if readJSON(filepath.Join(filepath.Dir(loc.CodexConfig), "auth.json"), &auth) == nil {
			key = first(text(auth[p.AuthEnv]), text(auth["OPENAI_API_KEY"]), key)
		}
	}
	return p, key
}
