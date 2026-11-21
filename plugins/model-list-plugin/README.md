# model-list-plugin

This plugin synchronizes a remote OpenAI or Anthropic compatible `/v1/models` response into the Codex model catalog and Claude Code model picker. An optional read-only metadata endpoint can add context windows, reasoning levels, modalities, aliases, and `behavesAs` values. It uses the Node runtime supplied by the host plugin system and has no package, daemon, young-router, or background-service dependency.

Install it in Claude Code with `/plugin install model-list-plugin --marketplace ysdj/model-list-plugin`.

Configure the provider with the included `model-list-configure` command, keep the API key in the selected environment variable, then run `model-list-enable`. The session hooks refresh the projections while the host is open. Use `model-list-status`, `model-list-sync`, `model-list-disable`, and `model-list-update` for operations. macOS and Windows use the same standard-library implementation.

Claude aliases default to `auto`: metadata aliases are honored and otherwise a stable `claude-mythos-<hash>` alias is generated. Use `--claude-alias-mode raw` when the provider expects its model IDs unchanged, or `derived` to always use plugin aliases.
