# model-list-plugin

Synchronize remote model metadata into Codex and Claude Code. The plugin includes macOS and Windows executables. See the [repository README](https://github.com/ysdj/model-list-plugin) for installation, configuration, hook trust, and synchronization behavior.

Install the plugin from Claude Code with `claude plugin install model-list-plugin --marketplace ysdj/model-list-plugin --scope user`. The first session discovers the configured provider and begins synchronization. Use `/model-list-plugin:model-list-plugin` to inspect or change the model source. The plugin checks for updates automatically.
