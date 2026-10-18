<div align="center">

# rig

A small, model-agnostic agent harness.

</div>

<br>

rig runs in the terminal and works with any model on [OpenRouter](https://openrouter.ai).
The TUI shows each request, each raw stream line and each token.

## Install

```sh
go install github.com/alfranz/rig/cmd/rig@latest
export OPENROUTER_API_KEY=sk-or-...
```

## Use

```sh
rig                        # TUI in the current directory
rig -p "explain main.go"   # one turn, plain text
rig -raw "hello"           # one request: the JSON out, every raw line back
```

| Flag | |
| --- | --- |
| `-agent <name>` | agent file to use (default `general`) |
| `-model <id>` | any OpenRouter model id; overrides `/model` and the agent file |
| `-yolo` | run `bash` and `edit_file` without asking |
| `-log-dir <dir>` | where session logs go (default `~/.rig/logs`) |

## Keys

| | |
| --- | --- |
| `enter` | send, or open the selected call |
| `tab` | move between the input and the calls pane |
| `↑ ↓` `j k` | select a call |
| `1` `2` `3` | request · stream · result |
| `y` `n` | allow or deny a tool call |
| `esc` | stop the agent, or close the detail view |
| `ctrl+c` | quit |

## Commands

Type `/` to open the menu.

| | |
| --- | --- |
| `/model` | pick a model: benchmarks, prices, your usage; `tab` sorts, `ctrl+o` opens the model page |
| `/context` | see what fills the context window |
| `/usage` | cost per day and per model, from all logs |
| `/clear` | start a new conversation |
| `/exit` | quit |

`/model` lists only models that support tool calls, because rig needs them.
It saves your pick in `~/.rig/state.json`, and the next start uses it.
`/context` measures against a 200k token "smart zone". Change it with `smart_zone_tokens` in the same file.

## Agents

An agent is a Markdown file. The YAML header picks the model and tools. The body is the system prompt.

```markdown
---
name: general
model: anthropic/claude-sonnet-5.5
tools: [read_file, list_files, edit_file, bash, code_search]
---
You are a coding agent ...
```

rig looks for `<name>.md` in `./.rig/agents`, then `~/.rig/agents`, then uses the copy inside the binary.
It also adds `AGENTS.md` (or `CLAUDE.md`) from the working directory to the system prompt.

## Logs

Each session writes one JSONL file to `~/.rig/logs`. A record holds the request, the raw lines with
time offsets, the final message, token usage and timings.

```sh
jq -c 'select(.type=="call") | {finish_reason, in: .usage.prompt_tokens, out: .usage.completion_tokens}' ~/.rig/logs/*.jsonl
```

## Inside

```
cmd/rig          flags, -p and -raw modes
internal/llm     API types, HTTP + SSE client, streamed tool calls
internal/agent   agent files, the loop, events
internal/tools   5 tools, hand-written JSON Schemas
internal/tui     Bubble Tea UI
internal/jsonl   session log, /usage totals
internal/state   ~/.rig/state.json
```

<br>

<div align="center"><sub>MIT · built by <a href="https://github.com/alfranz">@alfranz</a></sub></div>
