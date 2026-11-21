package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

type snapshot struct {
	path     string
	contents []byte
	exists   bool
}

func snapshots(paths ...string) ([]snapshot, error) {
	result := make([]snapshot, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		result = append(result, snapshot{path: path, contents: data, exists: err == nil})
	}
	return result, nil
}

func rollback(files []snapshot) error {
	var failures []error
	for _, file := range files {
		var err error
		if file.exists {
			err = writeFile(file.path, file.contents)
		} else {
			err = removeFile(file.path)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func transaction(loc locations, action func() error) error {
	files, err := snapshots(loc.CodexConfig, loc.CodexCatalog, loc.ClaudeSettings, loc.State, loc.Config)
	if err != nil {
		return err
	}
	if err := action(); err != nil {
		if restoreErr := rollback(files); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("could not restore previous files: %w", restoreErr))
		}
		return err
	}
	return nil
}

func syncModels(cfg config, loc locations, saved *state, host string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Second)
	defer cancel()
	modelsByHost := make(map[string][]model)
	var public *publicCache
	for _, target := range []struct {
		name    string
		enabled bool
	}{{"codex", cfg.Targets.Codex}, {"claude", cfg.Targets.Claude}} {
		if !target.enabled || (host != "" && host != target.name) {
			continue
		}
		p, key, err := resolveProvider(loc, target.name)
		var models []model
		if err == nil {
			models, err = fetchCatalog(ctx, p, key)
		}
		if err == nil && slices.ContainsFunc(models, func(m model) bool { return m.Window == 0 }) {
			if public == nil {
				loaded := loadPublicContexts(ctx, loc.Data)
				public = &loaded
			}
			models = fillPublicContexts(models, *public)
			if len(models) == 0 {
				err = errors.New("provider returned no conversation models")
			}
		}
		if err != nil {
			saved.LastError = err.Error()
			if saveErr := writeJSON(loc.State, saved); saveErr != nil {
				return 0, errors.Join(err, saveErr)
			}
			return 0, err
		}
		modelsByHost[target.name] = models
	}
	models := modelsByHost["codex"]
	if models == nil {
		models = modelsByHost["claude"]
	}
	if err := transaction(loc, func() error {
		doCodex := cfg.Targets.Codex && (host == "" || host == "codex")
		doClaude := cfg.Targets.Claude && (host == "" || host == "claude")
		if doCodex {
			if err := applyCodex(modelsByHost["codex"], loc, &saved.Managed); err != nil {
				return err
			}
		}
		if doClaude {
			if err := applyClaude(modelsByHost["claude"], loc, &saved.Managed, cfg.ClaudeAliasMode); err != nil {
				return err
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if saved.LastSyncAtByTarget == nil {
			saved.LastSyncAtByTarget = map[string]string{}
		}
		if doCodex {
			saved.LastSyncAtByTarget["codex"] = now
		}
		if doClaude {
			saved.LastSyncAtByTarget["claude"] = now
		}
		saved.LastSyncAt, saved.Version, saved.LastError = now, version, ""
		saved.ModelIDs = make([]string, 0, len(models))
		for _, m := range models {
			saved.ModelIDs = append(saved.ModelIDs, m.ID)
		}
		return writeJSON(loc.State, saved)
	}); err != nil {
		old, readErr := loadState(loc.State)
		if readErr == nil {
			old.LastError = err.Error()
			_ = writeJSON(loc.State, old)
			*saved = old
		}
		return 0, err
	}
	return len(models), nil
}

func disable(cfg config, loc locations, saved *state) error {
	return transaction(loc, func() error {
		if err := restoreCodex(loc, &saved.Managed); err != nil {
			return err
		}
		if err := restoreClaude(loc, &saved.Managed); err != nil {
			return err
		}
		cfg.Enabled = false
		cfg.InitializedTargets = map[string]bool{"codex": true, "claude": true}
		saved.LastError = ""
		if err := writeJSON(loc.State, saved); err != nil {
			return err
		}
		return writeJSON(loc.Config, cfg)
	})
}

func status(cfg config, loc locations, saved state, host string) object {
	safeURL := func(value string) string {
		if value == "" {
			return ""
		}
		u, err := endpoint(value)
		if err != nil {
			return "invalid URL"
		}
		u.RawQuery = ""
		return u.String()
	}
	sources := object{}
	for _, name := range []string{"codex", "claude"} {
		if host != "" && host != name {
			continue
		}
		p, key, err := resolveProvider(loc, name)
		source := object{"baseUrl": safeURL(p.BaseURL), "catalogUrl": safeURL(p.MetadataURL), "authConfigured": key != ""}
		if err != nil {
			source["error"] = err.Error()
		}
		sources[name] = source
	}
	return object{"version": version, "enabled": cfg.Enabled, "targets": cfg.Targets,
		"syncIntervalSeconds": cfg.SyncIntervalSeconds,
		"sources":             sources,
		"lastSyncAt":          saved.LastSyncAt, "lastSyncAtByTarget": saved.LastSyncAtByTarget, "lastError": saved.LastError, "modelCount": len(saved.ModelIDs), "modelIds": saved.ModelIDs,
		"lastUpdateCheckAtByHost": saved.LastUpdateCheckAtByHost, "lastUpdateErrorByHost": saved.LastUpdateErrorByHost,
		"paths": object{"config": loc.Config, "dataRoot": loc.Data, "codexCatalog": loc.CodexCatalog, "codexConfig": loc.CodexConfig, "claudeSettings": loc.ClaudeSettings}}
}

func parseArgs(args []string) (string, map[string]string, error) {
	command := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	options := make(map[string]string)
	for i := 0; i < len(args); i++ {
		item := args[i]
		if !strings.HasPrefix(item, "--") {
			return "", nil, fmt.Errorf("unexpected argument %q", item)
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(item, "--"), "=")
		if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			value, hasValue = args[i], true
		}
		if !hasValue {
			value = "true"
		}
		options[name] = value
	}
	return command, options, nil
}

func hostExecutable(host string) string {
	if path := os.Getenv("MODEL_LIST_PLUGIN_" + strings.ToUpper(host) + "_EXECUTABLE"); path != "" {
		return path
	}
	if path, err := exec.LookPath(host); err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	directories := []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"}
	if runtime.GOOS == "windows" {
		directories = []string{filepath.Join(home, ".local", "bin"), filepath.Join(os.Getenv("APPDATA"), "npm")}
	}
	names := []string{host}
	if runtime.GOOS == "windows" {
		names = []string{host + ".exe", host + ".cmd"}
	}
	var candidates []string
	for _, dir := range directories {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	if host == "codex" && runtime.GOOS == "darwin" {
		codexHome := first(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
		candidates = append(candidates, filepath.Join(codexHome, "plugins", "cache", "openai-bundled", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex"))
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			candidates = append(candidates, filepath.Join(root, "Codex.app", "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex"))
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return host
}

func hostExec(ctx context.Context, host string, args ...string) *exec.Cmd {
	executable := hostExecutable(host)
	ext := strings.ToLower(filepath.Ext(executable))
	if runtime.GOOS == "windows" && (ext == ".cmd" || ext == ".bat") {
		return exec.CommandContext(ctx, "cmd.exe", append([]string{"/d", "/c", executable}, args...)...)
	}
	return exec.CommandContext(ctx, executable, args...)
}

func updateHost(host string) error {
	var commands [][]string
	switch host {
	case "codex":
		commands = [][]string{{"plugin", "marketplace", "upgrade", pluginName}, {"plugin", "add", pluginName + "@" + pluginName}}
	case "claude":
		commands = [][]string{{"plugin", "marketplace", "update", pluginName}, {"plugin", "update", pluginName + "@" + pluginName}}
	default:
		return errors.New("host must be codex or claude")
	}
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		command := hostExec(ctx, host, args...)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("%s %s failed: %s", host, strings.Join(args, " "), strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func launchUpdate(host string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	directory := filepath.Join(os.TempDir(), "model-list-plugin-updaters")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	name := version + "-" + host
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	copyPath := filepath.Join(directory, name)
	if _, err := os.Stat(copyPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(copyPath, data, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	cmd := exec.Command(copyPath, "update", "--host", host, "--parent-pid", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func due(timestamp string, interval time.Duration) bool {
	last, err := time.Parse(time.RFC3339Nano, timestamp)
	return err != nil || time.Since(last) >= interval
}

func printJSON(value any) {
	output, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(output))
}

func run(args []string) error {
	command, options, err := parseArgs(args)
	if err != nil {
		return err
	}
	if command == "help" || options["help"] == "true" {
		fmt.Printf("model-list-plugin %s\nCommands: install enable disable sync status update uninstall hook\nOptions: --host codex|claude --force --defer\n", version)
		return nil
	}
	for option := range options {
		switch option {
		case "host", "force", "defer", "parent-pid":
		default:
			return fmt.Errorf("unknown option --%s", option)
		}
	}
	loc, err := locationsFor()
	if err != nil {
		return err
	}
	if command == "update" {
		host := first(options["host"], os.Getenv("MODEL_LIST_PLUGIN_HOST"), "claude")
		var updateErr error
		if value := options["parent-pid"]; value != "" {
			pid, parseErr := strconv.Atoi(value)
			if parseErr != nil || pid <= 0 {
				return errors.New("invalid update parent process")
			}
			updateErr = waitForProcess(pid)
		}
		if updateErr == nil {
			updateErr = updateHost(host)
		}
		release, lockErr := acquireLock(loc.Lock, 30*time.Second)
		if lockErr != nil {
			return errors.Join(updateErr, lockErr)
		}
		defer release()
		saved, stateErr := loadState(loc.State)
		if stateErr != nil {
			return errors.Join(updateErr, stateErr)
		}
		if saved.LastUpdateCheckAtByHost == nil {
			saved.LastUpdateCheckAtByHost = map[string]string{}
		}
		if saved.LastUpdateErrorByHost == nil {
			saved.LastUpdateErrorByHost = map[string]string{}
		}
		saved.LastUpdateCheckAtByHost[host] = time.Now().UTC().Format(time.RFC3339Nano)
		if updateErr != nil {
			saved.LastUpdateErrorByHost[host] = updateErr.Error()
		} else {
			delete(saved.LastUpdateErrorByHost, host)
		}
		return errors.Join(updateErr, writeJSON(loc.State, saved))
	}
	wait := 3 * time.Second
	if command == "hook" {
		wait = 0
	}
	release, err := acquireLock(loc.Lock, wait)
	if err != nil {
		if command == "hook" {
			return nil
		}
		return err
	}
	defer release()
	if err := migrateHostData(loc); err != nil {
		return err
	}
	cfg, err := loadConfig(loc.Config)
	if err != nil {
		return err
	}
	saved, err := loadState(loc.State)
	if err != nil {
		return err
	}
	switch command {
	case "install":
		if _, err := os.Stat(loc.Config); errors.Is(err, os.ErrNotExist) {
			if err := writeJSON(loc.Config, cfg); err != nil {
				return err
			}
		}
		printJSON(status(cfg, loc, saved, options["host"]))
	case "status":
		host := options["host"]
		if host != "" && host != "codex" && host != "claude" {
			return errors.New("host must be codex or claude")
		}
		printJSON(status(cfg, loc, saved, host))
	case "enable":
		host := options["host"]
		if host != "" && host != "codex" && host != "claude" {
			return errors.New("host must be codex or claude")
		}
		if host != "" {
			if cfg.InitializedTargets == nil {
				cfg.InitializedTargets = make(map[string]bool)
			}
			cfg.InitializedTargets[host] = true
			if !cfg.Enabled {
				cfg.Targets = targets{}
			}
			if host == "codex" {
				cfg.Targets.Codex = true
			} else {
				cfg.Targets.Claude = true
			}
		}
		cfg.Enabled = true
		if options["defer"] == "true" {
			if err := writeJSON(loc.Config, cfg); err != nil {
				return err
			}
			printJSON(status(cfg, loc, saved, host))
			return nil
		}
		var count int
		err := transaction(loc, func() error {
			var syncErr error
			count, syncErr = syncModels(cfg, loc, &saved, host)
			if syncErr != nil {
				return syncErr
			}
			return writeJSON(loc.Config, cfg)
		})
		if err != nil {
			return err
		}
		printJSON(object{"enabled": true, "modelCount": count, "status": status(cfg, loc, saved, host)})
	case "disable":
		host := options["host"]
		if host != "" {
			if host != "codex" && host != "claude" {
				return errors.New("host must be codex or claude")
			}
			if cfg.InitializedTargets == nil {
				cfg.InitializedTargets = make(map[string]bool)
			}
			cfg.InitializedTargets[host] = true
			if err := transaction(loc, func() error {
				if host == "codex" {
					if err := restoreCodex(loc, &saved.Managed); err != nil {
						return err
					}
					cfg.Targets.Codex = false
				} else {
					if err := restoreClaude(loc, &saved.Managed); err != nil {
						return err
					}
					cfg.Targets.Claude = false
				}
				cfg.Enabled = cfg.Targets.Codex || cfg.Targets.Claude
				if err := writeJSON(loc.State, saved); err != nil {
					return err
				}
				return writeJSON(loc.Config, cfg)
			}); err != nil {
				return err
			}
			printJSON(status(cfg, loc, saved, host))
			return nil
		}
		if err := disable(cfg, loc, &saved); err != nil {
			return err
		}
		printJSON(object{"enabled": false})
	case "sync":
		if !cfg.Enabled && options["force"] != "true" {
			return errors.New("plugin is disabled; use enable or sync --force")
		}
		host := options["host"]
		if host != "" && host != "codex" && host != "claude" {
			return errors.New("host must be codex or claude")
		}
		count, err := syncModels(cfg, loc, &saved, host)
		if err != nil {
			return err
		}
		printJSON(object{"synced": true, "modelCount": count, "status": status(cfg, loc, saved, host)})
	case "uninstall":
		if err := disable(cfg, loc, &saved); err != nil {
			return err
		}
		if err := removeFile(loc.Config); err != nil {
			return err
		}
		if err := removeFile(loc.State); err != nil {
			return err
		}
		printJSON(object{"uninstalled": true, "message": "Local configuration removed. Use the host plugin manager to remove the plugin."})
	case "hook":
		host := options["host"]
		if host != "codex" && host != "claude" {
			return errors.New("hook host must be codex or claude")
		}
		if !cfg.InitializedTargets[host] {
			if !cfg.Enabled {
				cfg.Targets = targets{}
			}
			if cfg.InitializedTargets == nil {
				cfg.InitializedTargets = make(map[string]bool)
			}
			cfg.InitializedTargets[host] = true
			cfg.Enabled = true
			if host == "codex" {
				cfg.Targets.Codex = true
			} else {
				cfg.Targets.Claude = true
			}
			if err := writeJSON(loc.Config, cfg); err != nil {
				return err
			}
		}
		enabledTarget := (host == "codex" && cfg.Targets.Codex) || (host == "claude" && cfg.Targets.Claude)
		if cfg.Enabled && enabledTarget && (options["force"] == "true" || due(saved.LastSyncAtByTarget[host], time.Duration(cfg.SyncIntervalSeconds)*time.Second)) {
			_, _ = syncModels(cfg, loc, &saved, host)
		}
		if due(saved.LastUpdateCheckAtByHost[host], 24*time.Hour) {
			if saved.LastUpdateCheckAtByHost == nil {
				saved.LastUpdateCheckAtByHost = map[string]string{}
			}
			saved.LastUpdateCheckAtByHost[host] = time.Now().UTC().Format(time.RFC3339Nano)
			if err := writeJSON(loc.State, saved); err == nil {
				if err := launchUpdate(host); err != nil {
					if saved.LastUpdateErrorByHost == nil {
						saved.LastUpdateErrorByHost = map[string]string{}
					}
					saved.LastUpdateErrorByHost[host] = err.Error()
					_ = writeJSON(loc.State, saved)
				}
			}
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "model-list-plugin:", err)
		os.Exit(1)
	}
}
