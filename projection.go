package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var pointerPattern = regexp.MustCompile(`^\s*model_catalog_json\s*=\s*("(?:\\.|[^"\\])*"|'[^']*')`)

func pointerLine(source string) (string, string) {
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		match := pointerPattern.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		value := match[1]
		if strings.HasPrefix(value, "'") {
			return value[1 : len(value)-1], line
		}
		decoded, err := strconv.Unquote(value)
		if err == nil {
			return decoded, line
		}
	}
	return "", ""
}

func setPointer(source, line string) string {
	lines := strings.Split(source, "\n")
	index := 0
	for index < len(lines) {
		trimmed := strings.TrimSpace(lines[index])
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		if pointerPattern.MatchString(lines[index]) {
			if line == "" {
				lines = append(lines[:index], lines[index+1:]...)
			} else {
				lines[index] = line
			}
			return strings.Join(lines, "\n")
		}
		index++
	}
	if line == "" {
		return source
	}
	lines = append(lines, "")
	copy(lines[index+1:], lines[index:])
	lines[index] = line
	return strings.Join(lines, "\n")
}

func nativeCatalog() []object {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := hostExec(ctx, "codex", "debug", "models", "--bundled").Output()
	if err != nil || len(out) > catalogByteLimit {
		return nil
	}
	var result struct {
		Models []object `json:"models"`
	}
	if json.Unmarshal(out, &result) == nil {
		return result.Models
	}
	return nil
}

func cloneObject(value object) object {
	data, _ := json.Marshal(value)
	var result object
	_ = json.Unmarshal(data, &result)
	return result
}

func codexCatalog(models []model, native []object) object {
	var base object
	for _, profile := range native {
		if (profile["base_instructions"] != nil || profile["model_messages"] != nil) && first(text(profile["visibility"]), "list") == "list" {
			base = profile
			break
		}
	}
	if base == nil && len(native) > 0 {
		base = native[0]
	}
	if base == nil {
		base = object{"base_instructions": "You are Codex, a coding agent.", "shell_type": "shell_command", "tool_mode": "direct", "supported_reasoning_levels": []object{{"effort": "medium", "description": "Balanced reasoning"}}, "default_reasoning_level": "medium"}
	}
	descriptions := map[string]string{"none": "No reasoning", "minimal": "Minimal reasoning", "low": "Lighter reasoning", "medium": "Balanced reasoning", "high": "Deeper reasoning", "xhigh": "Extra deep reasoning", "max": "Maximum reasoning", "ultra": "Maximum reasoning with delegation"}
	result := make([]object, 0, len(models))
	for i, m := range models {
		profile := base
		for _, candidate := range native {
			if text(candidate["slug"]) == m.ID {
				profile = candidate
				break
			}
		}
		entry := cloneObject(profile)
		exact := text(entry["slug"]) == m.ID
		entry["slug"], entry["display_name"], entry["priority"] = m.ID, m.Label, i+1
		entry["description"] = first(m.Description, text(entry["description"]), "Model exposed by the configured endpoint.")
		entry["visibility"], entry["supported_in_api"] = "list", true
		if !exact {
			entry["shell_type"], entry["tool_mode"] = "shell_command", "direct"
			delete(entry, "multi_agent_reasoning_effort")
		}
		if m.Window > 0 {
			entry["context_window"] = m.Window
		}
		if m.MaxWindow > 0 {
			entry["max_context_window"] = m.MaxWindow
		}
		if m.EffectivePercent > 0 {
			entry["effective_context_window_percent"] = m.EffectivePercent
		}
		if len(m.Inputs) > 0 {
			entry["input_modalities"] = m.Inputs
		}
		if len(m.Efforts) > 0 {
			levels := make([]object, 0, len(m.Efforts))
			for _, effort := range m.Efforts {
				levels = append(levels, object{"effort": effort, "description": descriptions[effort]})
			}
			entry["supported_reasoning_levels"] = levels
			chosen := m.DefaultEffort
			found := false
			for _, effort := range m.Efforts {
				if chosen == effort {
					found = true
				}
			}
			if !found {
				chosen = m.Efforts[0]
			}
			entry["default_reasoning_level"] = chosen
		}
		result = append(result, entry)
	}
	return object{"models": result}
}

func applyCodex(models []model, loc locations, managed *managedState) error {
	original, err := os.ReadFile(loc.CodexConfig)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pointer, line := pointerLine(string(original))
	if managed.Codex == nil {
		managed.Codex = &codexState{OriginalPointer: pointer, OriginalLine: line, HadConfig: exists, ManagedPath: loc.CodexCatalog}
	}
	if err := writeJSON(loc.CodexCatalog, codexCatalog(models, nativeCatalog())); err != nil {
		return err
	}
	return writeFile(loc.CodexConfig, []byte(setPointer(string(original), "model_catalog_json = "+strconv.Quote(loc.CodexCatalog))))
}

func restoreCodex(loc locations, managed *managedState) error {
	saved := managed.Codex
	if saved == nil {
		return nil
	}
	data, err := os.ReadFile(loc.CodexConfig)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, _ := pointerLine(string(data))
	if filepath.Clean(current) == filepath.Clean(saved.ManagedPath) {
		original := saved.OriginalLine
		if original == "" && saved.OriginalPointer != "" {
			original = "model_catalog_json = " + strconv.Quote(saved.OriginalPointer)
		}
		next := setPointer(string(data), original)
		if !saved.HadConfig && strings.TrimSpace(next) == "" {
			err = removeFile(loc.CodexConfig)
		} else {
			err = writeFile(loc.CodexConfig, []byte(next))
		}
		if err != nil {
			return err
		}
	}
	if err := removeFile(saved.ManagedPath); err != nil {
		return err
	}
	managed.Codex = nil
	return nil
}

func rawJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(left, right)
}

func claudeProjection(models []model, mode string) (json.RawMessage, map[string]json.RawMessage) {
	options := make([]object, 0, len(models))
	windows := make(map[string]json.RawMessage)
	for _, m := range models {
		id := m.ID
		if mode == "metadata" && m.ClaudeAlias != "" {
			id = m.ClaudeAlias
		}
		if mode == "derived" {
			sum := sha256.Sum256([]byte(id))
			id = "claude-mythos-" + hex.EncodeToString(sum[:])[:6]
		}
		behavior := m.BehavesAs
		if behavior == "" {
			switch {
			case m.Window >= 1000000:
				behavior = "claude-opus-5-5"
			case m.Window > 200000:
				behavior = "claude-opus-4-8"
			default:
				behavior = "claude-sonnet-4-6"
			}
		}
		row := object{"model": id, "label": m.Label, "behavesAs": behavior}
		if m.Description != "" {
			row["description"] = m.Description
		}
		options = append(options, row)
		if m.Window > 0 {
			windows[id] = rawJSON(object{"autoCompactWindow": max(100000, min(m.Window, 1000000))})
		}
	}
	return rawJSON(object{"replaceBuiltInOptions": true, "options": options}), windows
}

func readSettings(path string) (map[string]json.RawMessage, bool, error) {
	settings := make(map[string]json.RawMessage)
	_, err := os.Stat(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if err := readJSON(path, &settings); err != nil {
		return nil, false, err
	}
	if settings == nil {
		return nil, false, errors.New("Claude settings must be a JSON object")
	}
	return settings, exists, nil
}

func modelSettings(settings map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage)
	if value := settings["modelSettings"]; len(value) > 0 {
		if err := json.Unmarshal(value, &result); err != nil || result == nil {
			return nil, errors.New("Claude modelSettings must be an object")
		}
	}
	return result, nil
}

func restoreCompact(existing, original, last json.RawMessage) (json.RawMessage, bool, error) {
	current := make(map[string]json.RawMessage)
	previous := make(map[string]json.RawMessage)
	written := make(map[string]json.RawMessage)
	for _, item := range []struct {
		value  json.RawMessage
		target *map[string]json.RawMessage
	}{{existing, &current}, {original, &previous}, {last, &written}} {
		if len(item.value) > 0 {
			if err := json.Unmarshal(item.value, item.target); err != nil || *item.target == nil {
				return nil, false, errors.New("Claude modelSettings entry must be an object")
			}
		}
	}
	if len(last) > 0 && !sameJSON(current["autoCompactWindow"], written["autoCompactWindow"]) {
		return existing, existing != nil, nil
	}
	if value, ok := previous["autoCompactWindow"]; ok {
		current["autoCompactWindow"] = value
	} else {
		delete(current, "autoCompactWindow")
	}
	if len(current) == 0 && len(original) == 0 {
		return nil, false, nil
	}
	return rawJSON(current), true, nil
}

func applyClaude(models []model, loc locations, managed *managedState, mode string) error {
	settings, exists, err := readSettings(loc.ClaudeSettings)
	if err != nil {
		return err
	}
	existing, err := modelSettings(settings)
	if err != nil {
		return err
	}
	if managed.ClaudeSnapshot == nil {
		managed.ClaudeSnapshot = make(map[string]json.RawMessage)
		for k, v := range settings {
			managed.ClaudeSnapshot[k] = v
		}
		managed.ClaudeHadSettings = &exists
	}
	original, err := modelSettings(managed.ClaudeSnapshot)
	if err != nil {
		return err
	}
	picker, windows := claudeProjection(models, mode)
	for _, id := range managed.ClaudeAliases {
		if _, still := windows[id]; still {
			continue
		}
		value, keep, err := restoreCompact(existing[id], original[id], managed.ClaudeLastSettings[id])
		if err != nil {
			return err
		}
		if keep {
			existing[id] = value
		} else {
			delete(existing, id)
		}
	}
	for id, value := range windows {
		current := make(map[string]json.RawMessage)
		if existing[id] != nil {
			if err := json.Unmarshal(existing[id], &current); err != nil || current == nil {
				return fmt.Errorf("Claude modelSettings for %s must be an object", id)
			}
		}
		var projected map[string]json.RawMessage
		_ = json.Unmarshal(value, &projected)
		current["autoCompactWindow"] = projected["autoCompactWindow"]
		existing[id] = rawJSON(current)
		windows[id] = existing[id]
	}
	settings["modelPicker"] = picker
	if len(existing) > 0 || settings["modelSettings"] != nil {
		settings["modelSettings"] = rawJSON(existing)
	}
	managed.ClaudeLastPicker = picker
	managed.ClaudeLastSettings = windows
	managed.ClaudeAliases = managed.ClaudeAliases[:0]
	for id := range windows {
		managed.ClaudeAliases = append(managed.ClaudeAliases, id)
	}
	return writeJSON(loc.ClaudeSettings, settings)
}

func restoreClaude(loc locations, managed *managedState) error {
	if managed.ClaudeSnapshot == nil {
		return nil
	}
	settings, _, err := readSettings(loc.ClaudeSettings)
	if err != nil {
		return err
	}
	if len(managed.ClaudeLastPicker) == 0 || sameJSON(settings["modelPicker"], managed.ClaudeLastPicker) {
		if value, ok := managed.ClaudeSnapshot["modelPicker"]; ok {
			settings["modelPicker"] = value
		} else {
			delete(settings, "modelPicker")
		}
	}
	existing, err := modelSettings(settings)
	if err != nil {
		return err
	}
	original, err := modelSettings(managed.ClaudeSnapshot)
	if err != nil {
		return err
	}
	for _, id := range managed.ClaudeAliases {
		value, keep, err := restoreCompact(existing[id], original[id], managed.ClaudeLastSettings[id])
		if err != nil {
			return err
		}
		if keep {
			existing[id] = value
		} else {
			delete(existing, id)
		}
	}
	if len(existing) == 0 && managed.ClaudeSnapshot["modelSettings"] == nil {
		delete(settings, "modelSettings")
	} else {
		settings["modelSettings"] = rawJSON(existing)
	}
	if managed.ClaudeHadSettings != nil && !*managed.ClaudeHadSettings && len(settings) == 0 {
		err = removeFile(loc.ClaudeSettings)
	} else {
		err = writeJSON(loc.ClaudeSettings, settings)
	}
	if err != nil {
		return err
	}
	managed.ClaudeSnapshot, managed.ClaudeAliases, managed.ClaudeLastPicker, managed.ClaudeLastSettings, managed.ClaudeHadSettings = nil, nil, nil, nil, nil
	return nil
}
