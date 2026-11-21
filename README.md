# model-list-plugin

Synchronize a remote OpenAI-compatible or Anthropic-compatible model list and its available metadata into Codex and Claude Code. The plugin works with Young Router or another compatible provider.

The plugin includes executables for macOS (Apple Silicon and Intel) and Windows x64. Installation uses the agent's plugin manager and needs no local Young Router, separate runtime, or background service.

## Install

In Young Router, open **External settings**, select Codex or Claude, and choose **Install** beside **Model list plugin**. The form shows installation status, synchronization and automatic-update switches, settings, and uninstall. Enabling the Young Router API offers to install the plugin.

Alternatively install directly for each agent.

```sh
codex plugin marketplace add ysdj/model-list-plugin
codex plugin add model-list-plugin@model-list-plugin
claude plugin install model-list-plugin --marketplace ysdj/model-list-plugin --scope user
```

Start a new agent session. Codex may ask you to review and trust the plugin's hooks before it runs them. The first hook enables synchronization for that agent and discovers its provider. Young Router's install action also starts an immediate sync.

## Configure

The plugin discovers the selected Codex provider or Claude Code's `ANTHROPIC_BASE_URL` and authentication settings. Set a different base URL, optional metadata URL, and API-key environment variable name in Young Router's plugin settings or using Claude Code's `/model-list-plugin:model-list-configure` command. The URL can end in `/v1` or `/v1/models`. Key values remain in the agent's environment or existing settings.

Configuration is shared by both agents in `~/Library/Application Support/model-list-plugin/config.json` on macOS or `%APPDATA%/model-list-plugin/config.json` on Windows. Leave provider fields empty for discovery. The executable also supports `--kind auto|openai|anthropic`, `--auth-mode bearer|x-api-key|none`, `--interval SECONDS`, and `--auto-update true|false`.

The listing can provide context windows, reasoning levels, modalities and aliases. An optional metadata endpoint is matched by model ID and supplements the listing. A service that returns only IDs supplies no context or reasoning details. Young Router must run a version that exposes metadata in its listing. Claude uses provider model IDs by default. The optional `--claude-alias-mode metadata|derived` modes require the provider to accept those aliases.

## Synchronization and updates

Hooks run on session start and prompt submission, with a default minimum interval of 60 seconds per agent. Changes are fetched on a later hook invocation. The host may require a new session to reload a changed catalog or plugin version.

The plugin asks the host plugin manager to check for updates once per day by default while hooks run. Young Router exposes the automatic-update switch and last error. A failed refresh keeps the previous projection. Disabling restores the previous managed Codex pointer or Claude picker settings and preserves unrelated edits.

## Development

```sh
go test ./...
claude plugin validate .
claude plugin validate plugins/model-list-plugin
```

Build with Go 1.24 or later. The marketplace includes the macOS universal and Windows x64 executables under `plugins/model-list-plugin/bin`.
