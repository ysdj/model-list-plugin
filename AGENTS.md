# Working notes for this repository

Operational traps found while fixing the Claude Desktop context window.  Each
entry states the symptom, the cause that was measured, and the rule that avoids
it, so a later change does not have to rediscover them.

## Claude Desktop's context readout is not `modelSettings` alone

**Symptom.** `modelSettings.<id>.autoCompactWindow` says 500000, the session is
on that id, and the app still shows `x / 200K`.

**Cause.** The meter reads the CLI's own `get_context_usage` and takes
`rawMaxTokens` from it.  Claude Desktop starts every session with
`--settings '{"env":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}'`.  For a model
name Claude Code does not recognise, `rawMaxTokens` is pinned to that injected
number and no `modelSettings` value overrides it.  The name is only recognised
when `modelPicker.options` carries a row for it with `behavesAs`.

**Rule.** Write one `modelPicker` row per model, under the id both apps
address it by: the provider-published `claude_alias` when present (the derived
`claude-mythos-*` alias or a kept Claude id, plus `[1m]` at 1M), otherwise the
derived alias.  `modelSettings` states the exact number under the
picker id and the suffix-free id a session runs.  Do not add a row for every
spelling a previous release published: that turned thirteen models into
fifty-six rows and made the plugin's list impossible to reconcile with the
app's own.  A legacy id may keep its `modelSettings` entry, but a visible
legacy row is a targeted migration decision, not the default.

**Takeover.** Young Router's "Use Young Router API" switch enables the Claude
target and syncs once.  The canonical `/v1/models` supplies `claude_alias` and
`behaves_as`; the plugin must not re-derive them.  Generators are marked
`type: "image"` by the provider and are dropped before projection.  The app
must not write `modelPicker` or `modelSettings` again while the plugin owns
the file.

**How to verify without the GUI.** Drive the CLI over its stream-json channel
and read the same control response the app reads:

```sh
printf '{"type":"control_request","request_id":"c","request":{"subtype":"get_context_usage"}}\n{"type":"user","message":{"role":"user","content":"hi"},"parent_tool_use_id":null,"session_id":"x"}\n' |
  claude --output-format stream-json --verbose --input-format stream-json \
    --model claude-mythos-495a09 \
    --settings '{"env":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}' |
  grep -o '"rawMaxTokens": *[0-9]*'
```

`--settings` must carry the `env` injection, otherwise the probe reproduces a
different situation than the app's.  `modelUsage.contextWindow` in `-p` output
is a different number and did not track this fix; do not use it as evidence.

## Windows and the desktop app

- A process started from the Bash tool is killed by the tool's process-tree
  cleanup.  Start the app with
  `powershell -NoProfile -Command "Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine='\"<app path>\"'}"`.
- `CloseMainWindow()` does not exit it; `taskkill //IM claude.exe //F` does.
- The Claude hooks run `scripts/run.sh` through `sh`; a native Windows session
  does not put that on `PATH`, so each hook also states `commandWindows` that
  runs the shipped `.exe`.  Keep both variants in step when a hook argument
  changes.
- Mouse clicks issued through the accessibility tooling in this setup return
  `POLICY_DENIED`.  Clicking works through `SetCursorPos` + `mouse_event` from
  PowerShell, but the coordinates must be the ones UIA reports in physical
  pixels.  These are not the screenshot's pixel coordinates: screenshots come
  back at `scale_factor` 2.
- `desktop_set_value` inserts text without keystrokes, so a slash command typed
  this way never opens its menu.  Use it for ordinary prompts, and note that
  `/model <name>` is sent as a chat message, not executed.

## Building the shipped binaries

The marketplace ships both executables and both are committed:

```sh
go build -trimpath -ldflags="-s -w" -o plugins/model-list-plugin/bin/model-list-plugin.exe .
```

The macOS binary is a universal Mach-O (x86_64 + arm64).  `lipo` is not present
on the Windows checkout, so build both slices and assemble the fat header:

```sh
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/mac_amd64 .
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/mac_arm64 .
# then write a 0xcafebabe fat header with 16 KiB aligned slices
```

Read the slice `cputype`/`cpusubtype` little endian from each Mach-O; reading
them big endian produces a file `file(1)` cannot name.  Verify with
`file plugins/model-list-plugin/bin/model-list-plugin`, which must report both
architectures.

## Version bumps

Three files carry the version and a release must move all of them:
`store.go` (`const version`), `plugins/model-list-plugin/.claude-plugin/plugin.json`,
and `plugins/model-list-plugin/.codex-plugin/plugin.json`.  The Codex one is easy
to miss because it lives in its own hidden directory.

## Scratch paths and tools

- Git Bash `/tmp` and Python's `C:/tmp` are different directories.  Write
  temporary files under `$PI_SCRATCH_DIR` and pass absolute Windows paths to
  Python.
- This checkout is a shallow clone; `git fetch --unshallow` is needed before
  tagging a release, otherwise `git describe` and push refs are misleading.
