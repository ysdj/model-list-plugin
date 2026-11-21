---
name: model-list-plugin
description: Inspect, configure, or refresh the remote models and metadata synchronized by model-list-plugin in Codex or Claude Code.
---

The executable is at `bin/model-list-plugin` (`bin/model-list-plugin.exe` on Windows) under the plugin root, two directories above this skill's directory. Run it with the requested operation and separate, quoted arguments.

- `status` (default): report the model count, synchronization status, and any error. Do not include credentials.
- `configure --base-url URL --metadata-url URL --auth-env NAME`: change the shared model source. Each option is optional. An empty string clears that override and restores discovery from the client. Use an environment variable **name** for the API key.
- `enable --host HOST`, `disable --host HOST`, `sync --host HOST`: use `codex` or `claude` for the current client. Disabling restores its prior model settings.

Plugin updates run automatically while the host's hooks are active. Use the host's plugin manager to remove the plugin.
