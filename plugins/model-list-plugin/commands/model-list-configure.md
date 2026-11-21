---
description: Configure the remote OpenAI or Anthropic compatible model endpoint.
argument-hint: "--base-url URL [--kind auto|openai|anthropic] [--auth-env ENV] [--metadata-url URL] [--claude-alias-mode raw|metadata|derived]"
---

Use the arguments supplied by the user to run:

```sh
sh "${CLAUDE_PLUGIN_ROOT}/scripts/run.sh" configure <arguments>
```

The API key stays in the named environment variable. Never put a key in the plugin config or command output.
