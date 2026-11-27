# model-list-plugin

Synchronize a remote OpenAI-compatible or Anthropic-compatible model list and its available metadata into Codex and Claude Code. The plugin works with Young Router or another compatible provider.

The plugin includes executables for macOS (Apple Silicon and Intel) and Windows x64. Installation uses the agent's plugin manager and needs no local Young Router, separate runtime, or background service.

## Install

In Young Router, open **External settings**, select **Claude**, and choose **Install** beside **Model list plugin**. The card shows installation status and controls for syncing or uninstalling. Enabling the Young Router API offers to install the plugin. On a machine with only Claude Desktop, the card reaches the copy of Claude Code that app installed; no `claude` command on `PATH` is required. Codex manages this plugin from Codex itself (the Codex commands above); Young Router offers no card for it.

Alternatively install directly for each agent.

```sh
codex plugin marketplace add ysdj/model-list-plugin
codex plugin add model-list-plugin@model-list-plugin
claude plugin install model-list-plugin --marketplace ysdj/model-list-plugin --scope user
```

Start a new agent session. Codex may ask you to review and trust the plugin's hooks before it runs them. The first hook enables synchronization for that agent and discovers its provider. Young Router's install action also starts an immediate sync.

## Model source

Each sync reads the selected Codex provider from `config.toml` or the Claude Code gateway from `settings.json`. Change the API in the corresponding client configuration. In Young Router, turning off **Use Young Router API** opens the client's direct connection form. The form writes the client's settings. Earlier plugin source overrides are ignored.

The selected API supplies the model IDs and any metadata it publishes. When the client uses Young Router, the router's `/v1/models` returns the public context window from its own registry, including the value you set for a public model. A Codex provider with a `model_catalog_url` can supply additional metadata for its listed models. Claude Code has no corresponding field in its gateway configuration.

The key is taken from the same place the client reads it. A copy can lag behind: a gateway key replaced in the app, an `OPENAI_API_KEY` left in the environment from another install, or a `settings.json` edited by hand. When the API rejects the configured key, the sync retries with the credential the running session carries (Claude's `ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`, Codex's `OPENAI_API_KEY` or `auth.json`) and keeps whichever the endpoint accepts for the rest of that sync. A Claude session's key is only offered to the endpoint its own `ANTHROPIC_BASE_URL` names.

When a listed model has no context window, the plugin looks it up in the same public catalogs used by Young Router: Pi, the Codex catalog, and models.dev. The plugin caches this public data for six hours. Unmatched models use the client's defaults. Claude Code's auto-compaction window stays within its supported range.

Claude Code resolves a model's window from the picker row that names it: an id
it does not recognise falls back to 200000, and Claude Desktop pins that
fallback for the whole session. A session also keeps the id it was created
with, so the projection offers every spelling of a model — including the
aliases earlier releases generated — a row of its own, and states the window
under each of those ids.

## Synchronization and updates

Hooks run on session start and prompt submission, with a default minimum interval of 60 seconds per agent. Changes are fetched on a later hook invocation. The host may require a new session to reload a changed catalog or plugin version.

The plugin asks the host plugin manager to check for and install updates once per day while hooks run, including when model synchronization is paused. If automatic updates were turned off in v0.2.0, update once through the host's plugin manager to resume updates. A failed model fetch keeps the previous projection and records the error against that client only. Disabling synchronization restores the previous Codex catalog pointer or Claude picker settings and preserves unrelated edits.

## Development

```sh
go test ./...
claude plugin validate .
claude plugin validate plugins/model-list-plugin
```

Build with Go 1.24 or later. The marketplace includes the macOS universal and Windows x64 executables under `plugins/model-list-plugin/bin`.
