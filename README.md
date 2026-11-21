# model-list-plugin

`model-list-plugin` keeps the model pickers in Codex and Claude Code aligned with a remote OpenAI or Anthropic compatible `GET /v1/models` endpoint. It can also read a separate read-only metadata endpoint when a gateway publishes context windows, reasoning levels, aliases, or modalities there.

The implementation is a small Node standard-library script inside the plugin. It does not install young-router, npm packages, Python packages, a daemon, a LaunchAgent, a scheduled task, or a background service. The host agent invokes it through its normal plugin hooks on macOS and Windows.

## Install

Install from the public GitHub marketplace in each host you use.

```sh
# Claude Code
claude plugin marketplace add ysdj/model-list-plugin
claude plugin install model-list-plugin@model-list-plugin

# Codex
codex plugin marketplace add ysdj/model-list-plugin
codex plugin add model-list-plugin@model-list-plugin
```

The repository also contains a local marketplace checkout, so contributors can validate it without installing anything globally.

```sh
claude plugin marketplace add .
codex plugin marketplace add .
```

Restart the host after the first installation so its hook registry loads the plugin.

## Configure

The endpoint should be the provider base URL, its `/v1` URL, or its `/v1/models` URL. All three forms are normalized to one `GET /v1/models` request.

```sh
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" configure \
  --base-url https://gateway.example.com/v1 \
  --kind auto \
  --auth-env OPENAI_API_KEY \
  --targets codex,claude \
  --claude-alias-mode auto
```

For Anthropic compatible endpoints use `--kind anthropic` and an environment variable such as `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`. The key is read for the request and is never written to the plugin configuration, catalog, settings, logs, or Git repository.

An optional metadata endpoint can be configured when `/v1/models` does not carry the fields needed by the pickers.

```sh
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" configure \
  --metadata-url https://gateway.example.com/modelcatalog/list
```

Metadata rows may use `context_window`, `max_context_window`, `effective_context_window_percent`, `supported_efforts`, `default_reasoning_level`, `claude_alias`, `behaves_as`, `mode`, `modalities`, and the equivalent camel-case forms. The plugin also accepts the standard OpenAI `data` array and Anthropic `data` array. Unknown fields are ignored when projecting into either host.

Claude aliases default to `auto`, which uses a valid alias from metadata and otherwise creates a stable `claude-mythos-<hash>` alias. Set `--claude-alias-mode raw` to use the provider's model IDs directly, or `derived` to always generate plugin aliases.

The configuration is stored in the platform user data directory.

- macOS `~/Library/Application Support/model-list-plugin/config.json`
- Windows `%APPDATA%\\model-list-plugin\\config.json`
- Linux `$XDG_CONFIG_HOME/model-list-plugin/config.json`, or `~/.config/model-list-plugin/config.json`

`MODEL_LIST_PLUGIN_CONFIG` and `MODEL_LIST_PLUGIN_DATA` override these locations for tests and portable setups.

## Enable, inspect, and disable

```sh
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" install
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" enable
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" status
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" sync
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" disable
```

`enable` fetches and validates a non-empty model list before writing projections. A failed refresh leaves the last usable projection in place and records the error for `status`. `disable` restores the Codex catalog pointer and the Claude Code picker snapshot captured when the plugin was first enabled. `uninstall` removes those local projections and plugin data. The host plugin manager removes the installed plugin files themselves.

Codex receives a catalog at `~/.codex/model-list-plugin-catalog.json` and a `model_catalog_json` pointer in `~/.codex/config.toml`. Native Codex model profiles are read from the installed Codex CLI when available, then remote context and reasoning metadata is overlaid. A generic endpoint therefore works without copying a Linux young-router catalog to the client.

Claude Code receives `modelPicker.options` and per-alias `modelSettings`. A model with a one-million-token context uses the `[1m]` suffix and a matching `autoCompactWindow` entry. `modelPicker` support requires Claude Code 2.1.242 or later. User environment variables, labels, and credentials remain in the host settings.

## Synchronization and updates

The plugin has no resident process. Codex and Claude Code use `SessionStart` plus `UserPromptSubmit`. The default refresh interval is 60 seconds, which gives near-real-time updates while an agent is open and avoids network work on every prompt. Closing the host stops hook execution, so a new session is the next refresh boundary.

Host plugin managers own plugin upgrades. The hook checks once per day by default and runs the corresponding marketplace refresh command. A manual update is available as:

```sh
node "$CLAUDE_PLUGIN_ROOT/scripts/model-list-plugin.mjs" update --host claude
node "$PLUGIN_ROOT/scripts/model-list-plugin.mjs" update --host codex
```

For Codex, the update sequence refreshes the marketplace and asks Codex to install the selected marketplace version again. For Claude Code, it refreshes the marketplace and invokes the host plugin update command.

Codex or Claude Code may require a new session after an update. Codex can also ask for trust again when a plugin hook changes. The updater never replaces its own installed directory while it is running.

## Platform and security boundary

The code uses only URL, filesystem, crypto, and child-process APIs built into Node. It works with the Node runtime supplied by the host plugin system on macOS and Windows. It does not assume a local yr installation, a specific hostname, a provider key name, or a particular model family.

The plugin owns the client-side model-list switch and settings through `install`, `configure`, `enable`, `disable`, `sync`, `update`, and `uninstall`. It does not start, stop, install, or reconfigure a local young-router process. The endpoint is read-only and the optional metadata endpoint is also read-only.

## Development

```sh
node --test
claude plugin validate .
claude plugin validate plugins/model-list-plugin
```

The GitHub Actions workflow runs the same Node tests on Ubuntu, macOS, and Windows.
