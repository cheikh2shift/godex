# godex CLI/Agent Usage

## Running the agent

- Build or run directly: `go run ./cmd/godex`.
- The CLI expects a provider configuration YAML at `~/.godex/providers.yaml` by default; override with `--config /path/to/providers.yaml`.
- Launch `go run ./cmd/godex --wizard` to answer prompts and generate the YAML (or recreate it if you already have one).
- After configuration, the CLI drops into a styled TUI where you can type prompts, hit `Enter`, and view the provider's replies. Use  command `/quit` to exit.

## Provider configuration

The wizard writes a file similar to:

```yaml
providers:
  - name: gemini
    type: gemini
    model: gemini-2.5-flash
    description: Gemini text model
    api_key_env: GEMINI_API_KEY
    temperature: 0.2
    params:
      backend: gemini
default_provider: gemini
```

- `api_key_env` is the safest way to provide credentials; it reads whichever environment variable you configure. `api_key` writes a key directly into the YAML but is discouraged.
- `params.backend` controls the Gemini backend (`gemini` for the public Gemini API, or `vertex`/`vertexai` for Vertex AI). For Vertex add `params.project` and `params.location`.
- `ollama` providers use `endpoint` (or `params.base_url`) pointing to your local Ollama server and a `model` like `codeqwen:chat`.
- `huggingface` providers use `endpoint` (default `https://router.huggingface.co/v1`), a `model` like `deepseek-ai/DeepSeek-R1:fastest`, and `api_key_env` (default `HF_TOKEN`).
- Add more providers by copying entries under `providers:`. Each entry can supply a different `type`, `model`, and `params`.
- Use `--provider <name>` when launching the CLI to select a non-default provider from the YAML.

## Shell commands suggested by the AI

- The agent monitors Gemini's replies for lines prefixed with `RUN:`, `COMMAND:`, or `EXECUTE:` (case-insensitive). When it finds one, it stages that command and tells you what the AI wants to run.
- Enter `/confirm` in the input field to execute the staged command or `/cancel` to ignore it. While a command is waiting for confirmation, your text input stays in staging mode so you can’t immediately send another prompt.
- Each execution shows a `Command output` message (capturing stdout/stderr) and a `Diff` message that runs `git diff` so you can see every file change right away.
- The spinner serves as a “busy” indicator both when Gemini is generating responses and whenever the shell command is running.

## Contextual summaries

- After each Gemini response the agent automatically prompts the model to summarize that reply (three concise sentences and any file/code references). The summary is echoed in the UI, saved to `gtext/<session>.txt`, and noted with a “summary saved” system message.
- Summaries append to the session file (timestamped blocks) and the contents are read back before every new user prompt, so future questions include the distilled history (up to ~4 000 characters). This lets the agent “remember” earlier discoveries without you needing to re-share them.
- You can inspect the `gtext/` directory at any time to see the timeline of context summaries that the agent used for the current session.

## Project context requests

- The agent does not auto-send file contents. Instead, Gemini can request context using `READ: path1, path2`, `GREP: pattern | path1, path2`, or `TREE`.
- When Gemini asks, the agent returns chunked excerpts (or the tree) that stay under API limits, then asks Gemini again. This repeats up to three rounds before Gemini must answer normally.

## Verification loop

- After every main response, Gemini runs a 3‑round verification check. If the response is incomplete, it returns a short follow‑up question/action.
- Only once Gemini responds with `COMPLETE` does the UI surface any suggested command (and still requires `/confirm`).

## Extending providers

Providers are pluggable via the registry in `internal/providers`. To add a new provider, create a file that implements the `Provider` interface and calls `Register("type", factory)` in `init()`. The factory receives the YAML config so you can map any custom fields. `internal/agent.SendPrompt` simply resolves the provider by type and delegates the call, so new types require no TUI changes.
