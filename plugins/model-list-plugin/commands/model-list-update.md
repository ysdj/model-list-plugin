---
description: Ask the host plugin manager to refresh model-list-plugin.
---

Run the host update command and report its exit status:

```sh
node "${CLAUDE_PLUGIN_ROOT}/scripts/model-list-plugin.mjs" update --host claude
```

For Codex, use `--host codex`. Updates are applied by the host plugin manager and take effect in a later session.
