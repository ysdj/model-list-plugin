package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// sealedKeyPrefix marks a credential the Claude Desktop app stores encrypted
// with the operating system's own key store.  The app can unseal it; a plugin
// running beside the app cannot, so such a value is never sent anywhere.
const sealedKeyPrefix = "__sealed__:"

// claudeDesktopConfig is the deployment a Claude Desktop (3p) app was
// bootstrapped with.
//
// A Code session started by that app inherits its endpoint from the app, not
// from the user's settings.json: the file can carry an empty "env" block and no
// ANTHROPIC_BASE_URL at all.  A hook that runs inside such a session therefore
// has to read the app's own applied configuration, or it has no endpoint to
// name and can only fail.
type claudeDesktopConfig struct {
	BaseURL    string
	APIKey     string
	AuthScheme string
	SealedKey  bool
}

// desktopConfigDirs lists the places the Claude Desktop app keeps its applied
// deployment configuration, in the order they are trusted.
func desktopConfigDirs() []string {
	var dirs []string
	add := func(root string) {
		if root = strings.TrimSpace(root); root != "" {
			dirs = append(dirs, filepath.Join(root, "Claude-3p", "configLibrary"))
		}
	}
	if runtime.GOOS == "windows" {
		// The app keeps its deployment under %LOCALAPPDATA% on Windows;
		// os.UserConfigDir names %APPDATA% (Roaming), a different tree.  A
		// process started outside the user's own session (a service, a task)
		// carries another %LOCALAPPDATA%, so the profile's own copy is read too.
		add(os.Getenv("LOCALAPPDATA"))
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			add(filepath.Join(home, "AppData", "Local"))
		}
	}
	// macOS: <home>/Library/Application Support, Linux: $XDG_CONFIG_HOME or
	// <home>/.config.
	if configDir, err := os.UserConfigDir(); err == nil {
		add(configDir)
	}
	return dirs
}

// desktopGateway reads the first deployment configuration that names a gateway.
func desktopGateway() (claudeDesktopConfig, bool) {
	return desktopGatewayFrom(desktopConfigDirs())
}

// desktopGatewayFrom reads each configuration directory in turn.  A directory
// states which of its files is applied in _meta.json; without that statement
// the most recently written file is taken to be the applied one.
func desktopGatewayFrom(dirs []string) (claudeDesktopConfig, bool) {
	for _, dir := range dirs {
		for _, path := range appliedConfigPaths(dir) {
			var document struct {
				BaseURL    string `json:"inferenceGatewayBaseUrl"`
				APIKey     string `json:"inferenceGatewayApiKey"`
				AuthScheme string `json:"inferenceGatewayAuthScheme"`
			}
			if err := readJSON(path, &document); err != nil {
				continue
			}
			base := strings.TrimSpace(document.BaseURL)
			if base == "" {
				continue
			}
			key := strings.TrimSpace(document.APIKey)
			sealed := strings.HasPrefix(key, sealedKeyPrefix)
			if sealed {
				key = ""
			}
			return claudeDesktopConfig{
				BaseURL:    base,
				APIKey:     key,
				AuthScheme: strings.TrimSpace(document.AuthScheme),
				SealedKey:  sealed,
			}, true
		}
	}
	return claudeDesktopConfig{}, false
}

// appliedConfigPaths names the files in one configuration directory that may
// hold the applied deployment, most authoritative first.
func appliedConfigPaths(dir string) []string {
	var paths []string
	var meta struct {
		AppliedID string `json:"appliedId"`
	}
	if err := readJSON(filepath.Join(dir, "_meta.json"), &meta); err == nil {
		if id := strings.TrimSpace(meta.AppliedID); id != "" {
			paths = append(paths, filepath.Join(dir, id+".json"))
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return paths
	}
	type stamped struct {
		path    string
		modTime int64
	}
	var rest []stamped
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "_meta.json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		rest = append(rest, stamped{filepath.Join(dir, entry.Name()), info.ModTime().UnixNano()})
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].modTime > rest[j].modTime })
	for _, item := range rest {
		if len(paths) > 0 && paths[0] == item.path {
			continue
		}
		paths = append(paths, item.path)
	}
	return paths
}
