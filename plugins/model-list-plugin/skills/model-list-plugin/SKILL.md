---
name: model-list-plugin
description: Configure and operate model-list-plugin for remote model metadata in Codex and Claude Code.
---

# model-list-plugin

The plugin reads a configured OpenAI or Anthropic compatible `GET /v1/models` endpoint. An optional metadata URL can add context, reasoning, modality, Claude alias, and `behavesAs` fields. It writes a Codex catalog and Claude Code picker settings locally. It never needs a local young-router process.

Use the included commands for status, configuration, enable, disable, sync, and update. Credentials stay in the environment variable named by `provider.authEnv`.

The hook runs at session start and at prompt submission with a time limit. It does not run after the host process closes, so the next session is the refresh boundary.
