package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The same public sources, in the same priority order as Young Router's
// ModelContextRegistry. An API that publishes its own window needs none of these.
var publicSources = [...]string{
	"https://pi.dev/api/models",
	"https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/models.json",
	"https://models.dev/models.json",
}

type publicCache struct {
	FetchedAt time.Time
	Catalogs  [len(publicSources)]map[string]model
}

func publicRecords(payload any, source string) map[string]model {
	result := make(map[string]model)
	add := func(id string, fields object) {
		if !validID(id) || fields == nil {
			return
		}
		entry := model{ID: strings.ToLower(id)}
		entry.read(fields)
		limits := asObject(fields["limit"])
		entry.Window = intField(fields, "context_window", "contextWindow")
		if entry.Window == 0 {
			entry.Window = intField(limits, "context")
		}
		entry.MaxWindow = intField(fields, "max_context_window", "max_input_tokens")
		if entry.MaxWindow == 0 {
			entry.MaxWindow = intField(limits, "input")
		}
		if entry.Window == 0 {
			entry.Window = entry.MaxWindow
		}
		entry.MaxWindow = max(entry.Window, entry.MaxWindow)
		if entry.EffectivePercent == 0 {
			entry.EffectivePercent = 95
		}
		if entry.Window > 0 || entry.NonChat {
			result[entry.ID] = entry
		}
	}
	switch source {
	case publicSources[0]:
		for name, provider := range asObject(payload) {
			for _, value := range asObject(provider) {
				fields := asObject(value)
				if id := modelID(fields); validID(id) {
					add(name+"/"+id, fields)
				}
			}
		}
	case publicSources[1]:
		list, _ := rows(payload)
		for _, row := range list {
			add(row.id, row.fields)
		}
	case publicSources[2]:
		for id, value := range asObject(payload) {
			fields := asObject(value)
			if models := asObject(fields["models"]); models != nil {
				for name, entry := range models {
					row := asObject(entry)
					add(id+"/"+first(modelID(row), name), row)
				}
			} else {
				add(id, fields)
			}
		}
	}
	return result
}

type publicSourceResult struct {
	index int
	rows  map[string]model
}

func loadPublicContexts(ctx context.Context, data string) publicCache {
	path := filepath.Join(data, "public-contexts.json")
	var cache publicCache
	_ = readJSON(path, &cache) // An unreadable public cache is replaceable.
	if time.Since(cache.FetchedAt) < 6*time.Hour {
		return cache
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results := make(chan publicSourceResult, len(publicSources))
	for i, source := range publicSources {
		go func() {
			url, _ := endpoint(source)
			payload, _, err := fetchJSON(ctx, url, "none", "", "")
			var records map[string]model
			if err == nil {
				records = publicRecords(payload, source)
			}
			results <- publicSourceResult{i, records}
		}()
	}
	refreshed := false
	for range publicSources {
		result := <-results
		if len(result.rows) > 0 {
			cache.Catalogs[result.index] = result.rows
			refreshed = true
		}
	}
	if refreshed {
		cache.FetchedAt = time.Now()
		_ = writeJSON(path, cache)
	}
	return cache
}

func metadataIDs(id string) []string {
	id, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(id)), "@")
	id = strings.TrimPrefix(id, "models/")
	candidates := []string{id}
	if _, alias, ok := strings.Cut(id, "."); ok && !strings.Contains(id, "/") && alias != "" {
		candidates = append(candidates, alias)
	}
	for strings.Contains(id, "/") {
		_, id, _ = strings.Cut(id, "/")
		candidates = append(candidates, id)
	}
	return candidates
}

func (cache publicCache) lookup(id string) (model, bool) {
	ids := metadataIDs(id)
	for _, catalog := range cache.Catalogs {
		for _, candidate := range ids {
			if record, found := catalog[candidate]; found {
				return record, true
			}
		}
	}
	for _, catalog := range cache.Catalogs {
		var best model
		for key, record := range catalog {
			if !slices.ContainsFunc(ids, func(id string) bool { return strings.HasSuffix(key, "/"+id) }) {
				continue
			}
			// For two statements at the same priority use the narrower window.
			if best.ID == "" || comparePublicRecords(record, best) < 0 {
				best = record
			}
		}
		if best.ID != "" {
			return best, true
		}
	}
	return model{}, false
}

func comparePublicRecords(a, b model) int {
	first := [3]int{a.Window, a.MaxWindow, a.EffectivePercent}
	second := [3]int{b.Window, b.MaxWindow, b.EffectivePercent}
	if order := slices.Compare(first[:], second[:]); order != 0 {
		return order
	}
	return strings.Compare(a.ID, b.ID)
}

func fillPublicContexts(models []model, cache publicCache) []model {
	result := models[:0]
	for _, m := range models {
		if m.Window == 0 {
			if meta, ok := cache.lookup(m.ID); ok {
				if meta.NonChat {
					continue
				}
				m.Window, m.MaxWindow, m.EffectivePercent = meta.Window, meta.MaxWindow, meta.EffectivePercent
				if len(m.Efforts) == 0 {
					m.Efforts = meta.Efforts
					m.DefaultEffort = first(m.DefaultEffort, meta.DefaultEffort)
				}
				if m.Inputs == nil {
					m.Inputs = meta.Inputs
				}
			}
		}
		result = append(result, m)
	}
	return result
}
