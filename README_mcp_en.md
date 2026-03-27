# eDBG MCP

This document focuses on eDBG's MCP mode and the host-side installer workflow.

## Overview

- Phone-side `eDBG` exposes an HTTP MCP server
- The host forwards the port with `adb forward`
- The host-side `edbg-mcp-install` writes MCP configuration into AI clients
- The installer is a standalone host utility and is not shipped inside the Android `eDBG` binary

Default MCP URL:

```text
http://127.0.0.1:19810/mcp
```

## Start On The Device

```shell
adb push eDBG /data/local/tmp
adb shell
su
chmod +x /data/local/tmp/eDBG
/data/local/tmp/eDBG -p com.package.name -l libname.so --mcp
```

If `--mcp-port` is not specified, eDBG listens on `19810`.

Forward the port on the host:

```shell
adb forward tcp:19810 tcp:19810
```

## MCP Runtime Behavior

- `--mcp` forces `-prefer uprobe -show-vertual`.
- No startup breakpoint is installed automatically
- eDBG starts in standby mode and does not launch the target app by itself
- In standby, only `break`, `info_break`, `info_file`, breakpoint management, and `run` are allowed
- The MCP `break` tool always interprets the offset as a virtual offset and maps internally to `vbreak`
- `continue` blocks until a breakpoint is actually hit

Recommended flow:

1. Start phone-side `eDBG --mcp`
2. Run `adb forward tcp:19810 tcp:19810`
3. Install MCP config into your AI client
4. Use `break`
5. Use `run`
6. Use `continue`

## Building The Installer

The repository now includes a dedicated installer makefile:

```shell
make -f Makefile_installer current
make -f Makefile_installer all
```

Artifacts are written to:

```text
dist/installer/
```

By default, it builds installer binaries for these mainstream desktop targets:

- `darwin_amd64`
- `darwin_arm64`
- `linux_amd64`
- `linux_arm64`
- `windows_amd64`
- `windows_arm64`

## Installer Usage

```shell
./dist/installer/edbg-mcp-install --list-clients
./dist/installer/edbg-mcp-install --install
./dist/installer/edbg-mcp-install --install --clients codex,cursor,claude
./dist/installer/edbg-mcp-install --project --install --clients cursor,vscode,zed
./dist/installer/edbg-mcp-install --config
```

If you use a non-default forwarded port:

```shell
./dist/installer/edbg-mcp-install --install --url http://127.0.0.1:23456/mcp
```

To remove installed config:

```shell
./dist/installer/edbg-mcp-install --uninstall
./dist/installer/edbg-mcp-install --uninstall --clients codex,cursor
```

## Supported AI Clients

You can always inspect the live list with:

```shell
./dist/installer/edbg-mcp-install --list-clients
```

The current implementation covers major clients such as:

- Claude
- Claude Code
- Cursor
- VS Code
- VS Code Insiders
- Codex
- Cline
- Roo Code
- Kilo Code
- Windsurf
- Zed
- Gemini CLI
- Qwen Coder
- Copilot CLI
- Amazon Q
- LM Studio
- Opencode
- Warp
- Kiro
- Trae
- Augment Code
- Qodo Gen

## Notes

- The installer writes JSON or TOML depending on each client's config format
- Use `--project` for clients that support project-level MCP configuration
- Global installation only updates clients whose config directory already exists, to avoid polluting unrelated environments
- `Codex` is written into `~/.codex/config.toml`

