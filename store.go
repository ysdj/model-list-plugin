package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const version = "0.2.3"
const pluginName = "model-list-plugin"

type targets struct {
	Codex  bool `json:"codex"`
	Claude bool `json:"claude"`
}

type config struct {
	Enabled             bool            `json:"enabled"`
	Targets             targets         `json:"targets"`
	InitializedTargets  map[string]bool `json:"initializedTargets"`
	ClaudeAliasMode     string          `json:"claudeAliasMode"`
	SyncIntervalSeconds int             `json:"syncIntervalSeconds"`
}

func defaultConfig() config {
	return config{Targets: targets{true, true}, InitializedTargets: map[string]bool{}, ClaudeAliasMode: "raw", SyncIntervalSeconds: 60}
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

func codexProvider(path string) (provider, string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return provider{}, "", errors.New("could not read Codex config.toml")
	}
	var document struct {
		ModelProvider  string `toml:"model_provider"`
		OpenAIBaseURL  string `toml:"openai_base_url"`
		ModelProviders map[string]struct {
			BaseURL                 string `toml:"base_url"`
			EnvKey                  string `toml:"env_key"`
			ModelCatalogURL         string `toml:"model_catalog_url"`
			ExperimentalBearerToken string `toml:"experimental_bearer_token"`
			RequiresOpenAIAuth      bool   `toml:"requires_openai_auth"`
		} `toml:"model_providers"`
	}
	if toml.Unmarshal(data, &document) != nil {
		return provider{}, "", errors.New("Codex config.toml is not valid TOML")
	}
	selectedID := first(document.ModelProvider, "openai")
	selected := document.ModelProviders[selectedID]
	result := provider{
		BaseURL:     selected.BaseURL,
		Kind:        "openai",
		AuthEnv:     selected.EnvKey,
		AuthMode:    "bearer",
		MetadataURL: selected.ModelCatalogURL,
	}
	if selectedID == "openai" {
		result.BaseURL = first(document.OpenAIBaseURL, os.Getenv("OPENAI_BASE_URL"))
	}
	if result.AuthEnv != "" {
		key := strings.TrimSpace(os.Getenv(result.AuthEnv))
		if key == "" {
			return result, "", errors.New("the selected Codex provider's API key environment variable is empty")
		}
		result.Credentials = appendCredential(result.Credentials, credential{key, "bearer"})
		return result, key, nil
	}
	if selected.ExperimentalBearerToken != "" {
		key := strings.TrimSpace(selected.ExperimentalBearerToken)
		result.Credentials = appendCredential(result.Credentials, credential{key, "bearer"})
		return result, key, nil
	}
	if selected.RequiresOpenAIAuth || selectedID == "openai" {
		// The client itself reads this credential from auth.json, but the
		// session that runs this hook may carry an older OPENAI_API_KEY in its
		// environment.  Keep both: the listing request uses the one the
		// gateway accepts.
		result.Credentials = appendCredential(result.Credentials, credential{first(os.Getenv("OPENAI_API_KEY")), "bearer"})
		var auth struct {
			APIKey string `json:"OPENAI_API_KEY"`
		}
		if err := readJSON(filepath.Join(filepath.Dir(path), "auth.json"), &auth); err != nil && len(result.Credentials) == 0 {
			return result, "", errors.New("could not read the API key from Codex auth.json")
		}
		result.Credentials = appendCredential(result.Credentials, credential{first(auth.APIKey), "bearer"})
		if len(result.Credentials) > 0 {
			return result, result.Credentials[0].Value, nil
		}
	}
	return result, "", nil
}

// appendCredential adds one key to a provider's candidate list, skipping an
// empty value and a pair already there.  The first candidate is the one the
// client's own configuration names; the rest are what its session carries.
func appendCredential(list []credential, candidate credential) []credential {
	candidate.Value = strings.TrimSpace(candidate.Value)
	if candidate.Value == "" || slices.Contains(list, candidate) {
		return list
	}
	return append(list, candidate)
}

func resolveProvider(loc locations, host string) (provider, string, error) {
	switch host {
	case "codex":
		return codexProvider(loc.CodexConfig)
	case "claude":
		var settings struct {
			Env map[string]string `json:"env"`
		}
		if err := readJSON(loc.ClaudeSettings, &settings); err != nil {
			return provider{}, "", errors.New("could not read Claude Code settings.json")
		}
		configured := strings.TrimRight(first(settings.Env["ANTHROPIC_BASE_URL"]), "/")
		inherited := strings.TrimRight(first(os.Getenv("ANTHROPIC_BASE_URL")), "/")
		result := provider{BaseURL: first(configured, inherited), Kind: "anthropic", AuthMode: "x-api-key"}
		result.Credentials = appendCredential(result.Credentials, credential{first(settings.Env["ANTHROPIC_AUTH_TOKEN"]), "anthropic-token"})
		result.Credentials = appendCredential(result.Credentials, credential{first(settings.Env["ANTHROPIC_API_KEY"]), "x-api-key"})
		// A hook runs inside the client session, and that session exports the
		// credential it is using.  A key written in settings.json can be older
		// than the one the app holds (a gateway key replaced in the app, a file
		// edited by hand), so the session's value is kept as a fallback -- but
		// only when it names this same endpoint.  A session started on another
		// gateway must never hand its key to this URL.
		if inherited != "" && (configured == "" || inherited == configured) {
			result.Credentials = appendCredential(result.Credentials, credential{first(os.Getenv("ANTHROPIC_AUTH_TOKEN")), "anthropic-token"})
			result.Credentials = appendCredential(result.Credentials, credential{first(os.Getenv("ANTHROPIC_API_KEY")), "x-api-key"})
		}
		if len(result.Credentials) == 0 {
			return result, "", nil
		}
		primary := result.Credentials[0]
		result.AuthMode = primary.Mode
		return result, primary.Value, nil
	default:
		return provider{}, "", errors.New("host must be codex or claude")
	}
}
