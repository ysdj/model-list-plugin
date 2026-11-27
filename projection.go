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
	"sort"
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

// claudeWindowFloor is the smallest auto-compact window Claude Code accepts in
// modelSettings.  A smaller number is rejected, so a model whose provider
// states a narrower window is clamped up to it.
const claudeWindowFloor = 100000

// claudeWindowCeiling is the largest window the projection claims.  A model
// that declares more keeps its real window in every other catalog; the Claude
// projection stays inside the range a gateway credential can reach.
const claudeWindowCeiling = 1000000

// claudeBehavior picks the behaviour a row borrows when the provider states no
// "behaves_as".  The tiers are the client-side ones Young Router's picker uses:
// a route at or above 1M borrows the full 1M entry, a route between the 200k
// default and 1M borrows the large entry with the smaller output claim, and a
// route at or below 200k -- or one whose provider states no window -- borrows
// the default tier.  Keeping the two writers on the same rule is what makes a
// provider that states only a window produce the same row either way.
func claudeBehavior(window int) string {
	if window >= 1000000 {
		return "claude-opus-5-5"
	}
	if window > 200000 {
		return "claude-opus-4-8"
	}
	return "claude-sonnet-4-6"
}

// claudeCompactWindow is the auto-compact window written for one model, and
// whether the provider actually stated one.  The window is the one this
// projection states, so both the picker's behaviour and the entry are chosen
// from the same number: a model that declares more than the ceiling, such as a
// 1048576-token Gemini, has to borrow a behaviour that covers the claim rather
// than one matched to the raw figure.
func claudeCompactWindow(window int) (int, bool) {
	if window <= 0 {
		return 0, false
	}
	return max(claudeWindowFloor, min(window, claudeWindowCeiling)), true
}

// claudeModelID is the name a model is published under.  A provider that
// states claude_alias owns the spelling; every other route is derived from its
// public id, so there is one id space and no per-provider mode to drift.
func claudeModelID(raw model) string {
	if raw.ClaudeAlias != "" {
		return raw.ClaudeAlias
	}
	sum := sha256.Sum256([]byte(raw.ID))
	return "claude-mythos-" + hex.EncodeToString(sum[:])[:6]
}

var claudeDerivedPattern = regexp.MustCompile(`^(?:claude-mythos-|claude-fable-)[0-9a-f]{6}$`)

// claudeKeepsID reports whether the client already recognizes this id as its
// own.  A real Claude id keeps its label, effort ladder and behavior from the
// client catalog; a derived alias has none of those and needs a row that names
// the substitute.  The pattern is the same one Young Router uses for the two
// alias families it has published, so a real id such as “claude-fable-5-2“
// is not mistaken for an alias.
func claudeKeepsID(id string) bool {
	text := strings.ToLower(strings.TrimSuffix(id, "[1m]"))
	return strings.HasPrefix(text, "claude-") && !claudeDerivedPattern.MatchString(text)
}

// claudeDerivedAlias reports whether a modelSettings key is one of the derived
// spellings this plugin may have written.  A key under a public name, or one a
// person typed, is never retired here.
func claudeDerivedAlias(key string) bool {
	return claudeDerivedPattern.MatchString(strings.ToLower(strings.TrimSuffix(key, "[1m]")))
}

// claudeBehaviorFor chooses the behavior a row borrows.  A provider that
// states behaves_as is authoritative.  An id the client already knows keeps
// its own behavior; every other route borrows the tier for its window.
func claudeBehaviorFor(m model, id string) string {
	if m.BehavesAs != "" {
		return m.BehavesAs
	}
	if claudeKeepsID(id) {
		return ""
	}
	return claudeBehavior(m.Window)
}

func claudeOrdered(models []model) []model {
	ordered := append([]model(nil), models...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := first(ordered[i].Label, ordered[i].ID)
		right := first(ordered[j].Label, ordered[j].ID)
		if !strings.EqualFold(left, right) {
			return strings.ToLower(left) < strings.ToLower(right)
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

func claudeProjection(models []model) (json.RawMessage, map[string]json.RawMessage) {
	options := make([]object, 0, len(models))
	windows := make(map[string]json.RawMessage)
	for _, m := range claudeOrdered(models) {
		id := claudeModelID(m)
		window, stated := claudeCompactWindow(m.Window)
		if m.ClaudeAlias == "" && window >= 1000000 && !strings.HasSuffix(id, "[1m]") {
			id += "[1m]"
		}
		behavior := claudeBehaviorFor(m, id)
		row := object{"model": id}
		if !claudeKeepsID(id) {
			if m.Label != "" {
				row["label"] = m.Label
			}
			description := m.Description
			if description == "" {
				// Claude Code renders a subtitle for a curated row unless
				// description is present; the zero-width space keeps the row
				// reading as its model name, the same spelling Young Router's
				// picker writes.
				description = "\u200b"
			}
			row["description"] = description
		}
		if behavior != "" {
			row["behavesAs"] = behavior
		}
		options = append(options, row)
		// A model whose provider states no window is written with an empty
		// object rather than skipped: skipping it leaves a stale entry behind
		// for a row Claude Code no longer describes, and an absent entry sends
		// the session back to the 200000 Claude Code assumes for an
		// unrecognised name.
		if stated {
			windows[id] = rawJSON(object{"autoCompactWindow": window})
		} else {
			windows[id] = rawJSON(object{})
		}
		// The picker names the 1M row with the client's suffix, but a session
		// runs the suffix-free id.  The number has to be stated under both
		// spellings or the picker and the session disagree about the window.
		if base := strings.TrimSuffix(id, "[1m]"); base != id {
			windows[base] = windows[id]
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

func applyClaude(models []model, loc locations, managed *managedState) error {
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
	picker, windows := claudeProjection(models)
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
		if window, ok := projected["autoCompactWindow"]; ok && len(window) > 0 && string(window) != "null" {
			current["autoCompactWindow"] = window
		} else {
			delete(current, "autoCompactWindow")
		}
		existing[id] = rawJSON(current)
		windows[id] = existing[id]
	}
	// A derived id that names no current row is not a route the client can
	// send any more.  Retire it when it holds nothing but the window this
	// projection wrote; an entry with another field is the user's and stays.
	for key, value := range existing {
		if _, current := windows[key]; current {
			continue
		}
		if !claudeDerivedAlias(key) {
			continue
		}
		entry := make(map[string]json.RawMessage)
		if json.Unmarshal(value, &entry) != nil || entry == nil {
			continue
		}
		if _, window := entry["autoCompactWindow"]; len(entry) == 0 || (len(entry) == 1 && window) {
			delete(existing, key)
		}
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
