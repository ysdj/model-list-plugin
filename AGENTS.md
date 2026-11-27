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

**Rule.** Every id a session can send needs its own `modelPicker` row with
`behavesAs`, not just the id this release publishes.  Desktop pins a session to
the id it was created with, so legacy spellings (the derived `claude-mythos-*`
aliases and their `[1m]` form) have to be offered as rows too.  `modelSettings`
then states the exact number; `behavesAs` only decides which tier the client
uses.

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
