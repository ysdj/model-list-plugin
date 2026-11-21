package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type object = map[string]any

type provider struct {
	BaseURL          string `json:"baseUrl"`
	Kind             string `json:"kind"`
	AuthEnv          string `json:"authEnv"`
	AuthMode         string `json:"authMode,omitempty"`
	MetadataURL      string `json:"metadataUrl"`
	AnthropicVersion string `json:"anthropicVersion"`
}

func (p provider) authMode() string {
	if p.AuthMode == "bearer" || p.AuthMode == "none" {
		return p.AuthMode
	}
	if p.AuthMode == "anthropic-token" || (p.AuthMode == "" && p.AuthEnv == "ANTHROPIC_AUTH_TOKEN") {
		return "anthropic-token"
	}
	if p.AuthMode == "x-api-key" || p.Kind == "anthropic" || strings.Contains(strings.ToLower(p.AuthEnv), "anthropic") {
		return "x-api-key"
	}
	return "bearer"
}

type model struct {
	ID, Label, Description, ClaudeAlias, BehavesAs, DefaultEffort string
	Window, MaxWindow, EffectivePercent                           int
	Efforts, Inputs                                               []string
	NonChat                                                       bool
}

var efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

func asObject(value any) object {
	m, _ := value.(map[string]any)
	return m
}

func text(value any) string {
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func field(row object, names ...string) string {
	for _, name := range names {
		if s := text(row[name]); s != "" {
			return s
		}
	}
	return ""
}

func number(value any) int {
	if f, ok := value.(float64); ok && f > 0 && f <= 1<<31-1 {
		return int(f)
	}
	n, _ := strconv.ParseInt(strings.ReplaceAll(text(value), ",", ""), 10, 32)
	return max(int(n), 0)
}

func intField(row object, names ...string) int {
	for _, name := range names {
		if n := number(row[name]); n > 0 {
			return n
		}
	}
	return 0
}

func values(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	var list []any
	for _, s := range strings.FieldsFunc(text(value), func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		list = append(list, s)
	}
	return list
}

func canonEffort(value string) string {
	value = strings.ToLower(value)
	if value == "off" {
		return "none"
	}
	return value
}

func effortList(value any) []string {
	var result []string
	for _, item := range values(value) {
		s := text(item)
		if s == "" {
			s = field(asObject(item), "effort", "level", "name", "value")
		}
		s = canonEffort(s)
		if slices.Contains(efforts, s) && !slices.Contains(result, s) {
			result = append(result, s)
		}
	}
	slices.SortFunc(result, func(a, b string) int { return slices.Index(efforts, a) - slices.Index(efforts, b) })
	return result
}

func validID(id string) bool {
	return id != "" && !strings.ContainsFunc(id, unicode.IsControl)
}

func modelID(row object) string {
	return field(row, "id", "model", "slug", "model_id", "modelId", "public_name", "publicName", "name")
}

type modelRow struct {
	id     string
	fields object
}

func rows(payload any) ([]modelRow, bool) {
	if list, ok := payload.([]any); ok {
		var result []modelRow
		for _, item := range list {
			row := asObject(item)
			id := modelID(row)
			if row == nil {
				id = text(item)
			}
			if validID(id) {
				result = append(result, modelRow{id, row})
			}
		}
		return result, true
	}
	root := asObject(payload)
	for _, key := range []string{"data", "models", "items", "results", "result", "model_catalog", "modelCatalog", "catalog", "modelsById"} {
		value := root[key]
		if value == nil {
			continue
		}
		if result, ok := rows(value); ok {
			return result, true
		}
		if entries := asObject(value); len(entries) > 0 {
			var result []modelRow
			for id, item := range entries {
				if metadata := asObject(item); metadata != nil && validID(id) {
					result = append(result, modelRow{id, metadata})
				}
			}
			slices.SortFunc(result, func(a, b modelRow) int { return strings.Compare(a.id, b.id) })
			return result, len(result) > 0
		}
	}
	return nil, false
}

// Fill only fields the source supplies, leaving higher-priority values intact.
func (m *model) read(row object) {
	parts := []object{row}
	for _, key := range []string{"metadata", "model_info", "modelInfo", "model_context", "modelContext", "capabilities", "context", "limit", "limits", "architecture"} {
		if part := asObject(row[key]); part != nil {
			parts = append(parts, part)
		}
	}
	for _, p := range parts {
		if m.Label == "" {
			m.Label = field(p, "display_name", "displayName", "label", "name")
		}
		if m.Description == "" {
			m.Description = field(p, "description")
		}
		if m.ClaudeAlias == "" {
			m.ClaudeAlias = field(p, "claude_alias", "claudeAlias", "anthropic_alias", "anthropicAlias")
		}
		if m.BehavesAs == "" {
			m.BehavesAs = field(p, "behaves_as", "behavesAs")
		}
		if m.Window == 0 {
			m.Window = intField(p, "context_window", "contextWindow", "context_length", "contextLength", "max_input_tokens", "maxInputTokens", "input_token_limit", "inputTokenLimit", "context", "input")
		}
		if m.MaxWindow == 0 {
			m.MaxWindow = intField(p, "max_context_window", "maxContextWindow", "max_context_tokens", "maxContextTokens")
		}
		if m.EffectivePercent == 0 {
			if percent := intField(p, "effective_context_window_percent", "effectiveContextWindowPercent"); percent <= 100 {
				m.EffectivePercent = percent
			}
		}
		reasoning := asObject(p["reasoning"])
		if m.DefaultEffort == "" {
			m.DefaultEffort = canonEffort(field(p, "default_reasoning_level", "defaultReasoningLevel", "default_reasoning_effort", "default_effort", "defaultEffort", "reasoning_effort", "reasoningEffort"))
			if m.DefaultEffort == "" {
				m.DefaultEffort = canonEffort(field(reasoning, "default", "default_effort", "defaultEffort", "effort"))
			}
		}
		if m.Efforts == nil {
			for _, key := range []string{"supported_efforts", "supportedEfforts", "supported_reasoning_levels", "supportedReasoningLevels", "supported_reasoning_efforts", "supportedReasoningEfforts", "reasoning_efforts", "reasoningEfforts", "reasoning_levels", "reasoningLevels", "reasoning"} {
				if levels := effortList(p[key]); len(levels) > 0 {
					m.Efforts = levels
					break
				}
			}
			for _, key := range []string{"supported", "supported_efforts", "supportedEfforts", "efforts", "levels", "supported_levels", "supportedLevels", "effort", "level"} {
				if m.Efforts == nil {
					m.Efforts = effortList(reasoning[key])
				}
			}
			if m.Efforts == nil {
				mapping := asObject(p["thinkingLevelMap"])
				if mapping == nil {
					mapping = asObject(p["thinking_level_map"])
				}
				for _, level := range efforts {
					name := level
					if level == "none" {
						name = "off"
					}
					if text(mapping[name]) != "" {
						m.Efforts = append(m.Efforts, level)
					}
				}
			}
			if m.Efforts == nil && (p["reasoning"] == true || p["supports_reasoning"] == true) {
				m.Efforts = []string{"low", "medium", "high"}
			}
		}
		if m.Inputs == nil {
			for _, key := range []string{"input_modalities", "inputModalities", "modalities", "input"} {
				items := p[key]
				if key == "modalities" && asObject(items) != nil {
					items = asObject(items)["input"]
				}
				for _, input := range values(items) {
					name := strings.ToLower(text(input))
					if (name == "text" || name == "image") && !slices.Contains(m.Inputs, name) {
						m.Inputs = append(m.Inputs, name)
					}
				}
				if m.Inputs != nil {
					break
				}
			}
		}
		m.NonChat = m.NonChat || nonChat(p)
	}
}

func nonChat(p object) bool {
	if p["conversation"] == false {
		return true
	}
	if p["image_generation"] == true || p["video_generation"] == true {
		return true
	}
	chatMode := false
	specialMode := false
	if modalities := asObject(p["modalities"]); modalities != nil && nonChat(object{"output_modalities": modalities["output"]}) {
		return true
	}
	for _, key := range []string{"mode", "modes", "task", "type", "model_type", "modelType", "generation_type", "output_modalities", "outputModalities"} {
		special := false
		for _, value := range values(p[key]) {
			switch strings.ReplaceAll(strings.ToLower(text(value)), "-", "_") {
			case "text", "chat", "responses", "messages", "completion", "text_generation":
				chatMode = true
			case "image", "video", "image_generation", "video_generation", "embedding", "embeddings", "rerank", "moderation", "text_to_speech", "transcription", "audio_generation":
				special = true
			}
		}
		specialMode = specialMode || special
	}
	return specialMode && !chatMode
}

func normalize(list, metadata []modelRow) []model {
	byID := make(map[string]object, len(metadata))
	for _, row := range metadata {
		byID[row.id] = row.fields
	}
	var result []model
	seen := make(map[string]bool, len(list))
	for _, row := range list {
		if seen[row.id] {
			continue
		}
		seen[row.id] = true
		m := model{ID: row.id}
		m.read(row.fields)
		if fields := byID[row.id]; fields != nil && !m.NonChat {
			window, maximum, percent := m.Window, m.MaxWindow, m.EffectivePercent
			m.read(fields)
			if window != 0 || maximum != 0 {
				m.Window, m.MaxWindow, m.EffectivePercent = window, maximum, percent
			}
		}
		m.Label = first(m.Label, m.ID)
		if m.Window == 0 {
			m.Window = m.MaxWindow
		}
		if m.Window > m.MaxWindow {
			m.MaxWindow = m.Window
		}
		if !m.NonChat {
			result = append(result, m)
		}
	}
	return result
}

func endpoint(value string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("endpoint must be an absolute HTTP(S) URL without URL credentials or a fragment")
	}
	return u, nil
}

func listingURL(u *url.URL) *url.URL {
	result := *u
	result.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(result.Path, "/models") {
		if !strings.HasSuffix(result.Path, "/v1") {
			result.Path += "/v1"
		}
		result.Path += "/models"
	}
	return &result
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

var catalogHTTP = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const catalogByteLimit = 16 << 20

func fetchJSON(ctx context.Context, u *url.URL, auth, key, anthropicVersion string) (any, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, errors.New("invalid catalog request")
	}
	req.Header.Set("Accept", "application/json")
	if auth == "x-api-key" || auth == "anthropic-token" {
		req.Header.Set("anthropic-version", first(anthropicVersion, "2023-06-01"))
	}
	if key != "" {
		if auth == "x-api-key" {
			req.Header.Set("x-api-key", key)
		} else if auth != "none" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	res, err := catalogHTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, errors.New("catalog request timed out")
		}
		return nil, 0, fmt.Errorf("could not reach catalog host %s", u.Hostname())
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("catalog request returned HTTP %d", res.StatusCode)
	}
	var result any
	body, err := io.ReadAll(io.LimitReader(res.Body, catalogByteLimit+1))
	if err != nil || len(body) > catalogByteLimit || json.Unmarshal(body, &result) != nil {
		return nil, res.StatusCode, errors.New("catalog response is not a valid JSON document within 16 MiB")
	}
	return result, res.StatusCode, nil
}

func fetchCatalog(ctx context.Context, p provider, key string) ([]model, error) {
	base, err := endpoint(p.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("the client configuration needs a model API URL: %w", err)
	}
	u := listingURL(base)
	auth := p.authMode()
	var list []modelRow
	cursors := map[string]bool{}
	for {
		payload, status, err := fetchJSON(ctx, u, auth, key, p.AnthropicVersion)
		if err != nil {
			// A provider with a Codex-native catalog may implement inference
			// and that catalog without implementing OpenAI model discovery.
			if len(list) == 0 && p.MetadataURL != "" && (status == 404 || status == 405) {
				break
			}
			return nil, fmt.Errorf("/v1/models: %w", err)
		}
		page, ok := rows(payload)
		if !ok {
			return nil, errors.New("/v1/models did not return a model list")
		}
		list = append(list, page...)
		if asObject(payload)["has_more"] != true {
			break
		}
		cursor := field(asObject(payload), "last_id")
		if cursor == "" || cursors[cursor] {
			return nil, errors.New("/v1/models returned a missing or repeated pagination cursor")
		}
		cursors[cursor] = true
		query := u.Query()
		query.Set("after_id", cursor)
		u.RawQuery = query.Encode()
	}
	var supplemental []modelRow
	if p.MetadataURL != "" {
		meta, err := endpoint(p.MetadataURL)
		if err != nil {
			return nil, fmt.Errorf("client model_catalog_url: %w", err)
		}
		metaKey := ""
		if sameOrigin(base, meta) {
			metaKey = key
		}
		payload, _, err := fetchJSON(ctx, meta, auth, metaKey, p.AnthropicVersion)
		if err != nil {
			return nil, fmt.Errorf("client model_catalog_url: %w", err)
		}
		var ok bool
		if supplemental, ok = rows(payload); !ok {
			return nil, errors.New("client model_catalog_url did not return a model list")
		}
		if len(list) == 0 {
			list, supplemental = supplemental, nil
		}
	}
	result := normalize(list, supplemental)
	if len(result) == 0 {
		return nil, errors.New("provider returned no conversation models")
	}
	return result, nil
}
