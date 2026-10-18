# AGENTS.md

`rig` is a small coding agent in Go. It talks to the OpenRouter chat completions API with plain `net/http` and streaming (SSE).
The goal is learning, so the code stays small and readable, and the TUI shows every raw API call.
`README.md` has the user guide. `PLAN.md` has the design and its decisions.

## Commands

```sh
make check                      # gofmt, go vet, go test: run before you say a change is done
make                            # list all make targets
go install ./cmd/rig            # updates the `rig` binary in ~/go/bin
rig -raw "hi"                   # one real API call: prints the request JSON and every raw stream line
rig -p "question" </dev/null    # one real turn in plain text; bash/edit_file are denied (no stdin)
```

Real API calls need `OPENROUTER_API_KEY` and cost money. Use the unit tests first. Keep real calls short.

## Layout

- `cmd/rig/main.go`: flags, the `-raw` and `-p` modes, starts the TUI.
- `internal/llm`: API types, `Client.Stream` (HTTP + SSE parser), assembles streamed tool calls by `index`.
- `internal/agent`: agent files (`.rig/agents/<name>.md`, YAML header + system prompt), the loop `Session.Send`, events.
  The system prompt also gets `AGENTS.md` from the working directory, or `CLAUDE.md` when there is no `AGENTS.md`.
- `internal/tools`: `read_file`, `list_files`, `edit_file`, `bash`, `code_search`. Each has a hand-written JSON Schema string.
- `internal/tui`: Bubble Tea v1 UI. `tui.go` has state and input, `view.go` has rendering.
- `internal/jsonl`: session log in `~/.rig/logs/<time>.jsonl`.
- `embed.go`: embeds `.rig/agents/*.md`, so `rig` works in any directory.

## Rules

- No LLM SDKs. Use `net/http` and our own structs, so the wire format stays visible.
- The core never imports the UI. The loop sends `agent.Event` values, and the TUI and the log only read them.
  A new feature that the UI must show needs a new event type in `internal/agent/events.go`.
- Send the `tools` array on every request. Every assistant `tool_calls` entry needs a `tool` message with the same `tool_call_id` before the next request.
- Tool errors go back to the model as the tool result text (`error: ...`). Do not stop the loop for them.
- Never send an empty tool result. Some providers reject it, so use `(no output)`.
- Tools that change files or run commands set `NeedsApproval: true`.
- Request JSON is encoded with `SetEscapeHTML(false)`, so the inspector shows `<` and `>` as plain text.

## TUI notes

- Mouse clicks map screen rows to calls: border row 0, title row 1, first call at row 2.
  Every line in a pane must fit on one screen row. Truncate with `ansi.Truncate`, and replace tabs before you measure width.
- Never call a `Session` method that emits events from inside `Update`. `Emit` calls `p.Send`, which blocks there.
  Run it in a `tea.Cmd`, and update the UI when the event arrives (see `cmdClear`).
- `internal/tui/tui_test.go` checks the layout at 3 terminal sizes (`checkFits`). Add cases there for new UI.
- To drive the real TUI in a pseudo-terminal, the test must answer the startup queries `ESC]11;?` (background color)
  and `ESC[6n` (cursor position). If it does not, Lip Gloss reads the typed keys as the answer.

## Style

- Comments explain why, in short plain English. Match the existing comment density.
- Go 1.24: use `min`, `max`, and `for range n`.
