---
name: model-list-plugin
description: Inspect or refresh the models and metadata from the API configured in Codex or Claude Code.
---

The executable is at `bin/model-list-plugin` (`bin/model-list-plugin.exe` on Windows) under the plugin root, two directories above this skill's directory. Run it with the requested operation and separate, quoted arguments.

- `status --host HOST` (default): report the model count, synchronization status, and any error. Do not include credentials.
- `enable --host HOST`, `disable --host HOST`, `sync --host HOST`: use `codex` or `claude` for the current client. Disabling restores its prior model settings.

The model source follows the current client's configuration. Plugin updates run automatically while the host's hooks are active. Use the host's plugin manager to remove the plugin.
