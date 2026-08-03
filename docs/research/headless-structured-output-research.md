# Headless CLI structured output: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. Re-verify before adopting; the CLIs and their
> documentation change quickly.
>
> Decision surface: how deployed agent CLIs expose a machine-readable final
> result, and whether they let the caller constrain that result with JSON
> Schema.
>
> Scope: non-interactive/headless invocation, final-result envelopes, JSONL
> event streams, schema declaration, and the boundary between transport JSON
> and validated structured output.
>
> Out of scope: model-provider APIs, SDK interfaces, prompt-only conventions,
> private implementation details, and this repository's implementation.

## 1. Conclusions

1. **Documented fact:** the three deployed CLIs expose materially different
   contracts. Codex CLI 0.146.0 exposes an `--output-schema <FILE>` option for
   the final response and a separate `--json` JSONL event mode. Claude Code
   2.1.212 exposes `--json-schema` in print mode and documents a structured
   result inside its JSON response envelope. Gemini CLI 0.44.1 exposes
   `text`, `json`, and `stream-json`, but the reviewed CLI and official headless
   reference do not expose a JSON-Schema constraint.

2. **Cross-product synthesis:** “JSON output” is not one interoperability
   contract. In the evidence reviewed, it can mean (a) an event transport
   containing lifecycle/tool/session data, (b) a single envelope containing a
   textual answer plus usage metadata, or (c) an envelope with a separately
   validated structured result. A consumer must identify the final-result
   field and validation semantics rather than merely parse JSON.

3. **Cross-product synthesis:** schema control is concentrated on the final
   answer, not on every intermediate agent event. Claude states this boundary
   explicitly through print-mode structured output; Codex's help describes the
   schema as describing the model's “final response”; Gemini's documented JSON
   shape keeps `response` as a string. This leaves event streams and final
   structured values as separate concerns.

4. **Evidence gap:** the public surfaces do not establish a common guarantee
   about schema dialect/version, supported keywords, retry or repair behavior,
   refusal/partial-result semantics, or whether a schema failure changes the
   process exit status in every mode. Those properties must not be inferred
   from the presence of a flag.

## 2. Evidence from deployed applications

### 2.1 OpenAI Codex CLI

**Documented fact — observed shipped help.** On 2026-08-04, the installed
`codex-cli 0.146.0` reported the following relevant `codex exec --help`
surface:

| Surface | What the product documents |
| --- | --- |
| `--output-schema <FILE>` | A path to a JSON Schema file “describing the model's final response shape.” |
| `--json` | Print events to stdout as JSONL. |
| `--output-last-message <FILE>` | Write the last message to a file; this is a delivery option, not a schema declaration. |

The wording explicitly places the schema on the final response, while
`--json` is described as an event serialization mode. The help does not, by
itself, state the JSONL envelope schema, supported JSON-Schema dialect, or
whether validation is provider-enforced, client-enforced, retried, or merely
requested from the model.

**Documented fact — public reference.** OpenAI publishes the Codex CLI
reference and lists `codex exec` as the non-interactive execution surface; the
current local binary is the stronger version-specific evidence for the exact
flag wording used here. [Codex CLI reference](https://developers.openai.com/codex/cli/reference/)

**Evidence boundary.** This evidence supports “Codex has a final-response
JSON-Schema option” and “Codex has JSONL event output.” It does not support a
claim that all events conform to the requested schema or that the schema is
enforced by the underlying model provider.

### 2.2 Anthropic Claude Code

**Documented fact — observed shipped help.** On 2026-08-04, the installed
`Claude Code 2.1.212` help reported:

- `-p, --print` runs a response and exits for non-interactive use.
- `--output-format <format>` is available in print mode with `text`, `json`, or
  `stream-json`.
- `--json-schema <schema>` is described as “JSON Schema for structured output
  validation,” with an inline object example.
- The help describes `--json-schema` as a print-mode facility rather than an
  interactive-session output setting.

**Documented fact — official headless documentation.** Anthropic's Claude Code
headless documentation describes JSON output as a single result object and
describes `structured_output` as the schema-shaped result when `--json-schema`
is supplied. The outer result still carries session and execution metadata;
therefore the schema-shaped value is not the same thing as replacing the
entire process envelope with an arbitrary object. The documentation also
describes schema validation errors as failures rather than silently treating an
invalid schema as ordinary text.

Sources: [Claude Code headless mode](https://code.claude.com/docs/en/headless.md),
[Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference.md).

**Evidence boundary.** The sources establish a product-level structured-output
contract in print mode. They do not, in the material reviewed, establish that
the schema governs intermediate tool events in `stream-json`, nor do they
fully enumerate the accepted JSON-Schema dialect or all failure/refusal
shapes.

### 2.3 Google Gemini CLI

**Documented fact — observed shipped help.** On 2026-08-04, the installed
`gemini 0.44.1` help exposed `--output-format` choices `text`, `json`, and
`stream-json`. It did not list a `--json-schema` or equivalent CLI option.
The help also documents `-p/--prompt` as headless mode.

**Documented fact — official headless reference.** Gemini CLI's official
headless reference says headless mode returns structured text or JSON. Its
documented JSON output is one object containing:

```json
{
  "response": "<the model's final answer>",
  "stats": {},
  "error": {}
}
```

The page documents `response` as a string and `stream-json` as newline-
delimited event output. It does not document a caller-supplied JSON Schema
that constrains the final response object.

Source: [Gemini CLI headless mode reference](https://geminicli.com/docs/cli/headless/)
(the page's “JSON output” and “Streaming JSON output” sections).

**Evidence boundary.** Absence of a schema flag in one version's help and
reference is evidence about the reviewed CLI surface, not proof that no
extension, model-specific feature, or future release can produce structured
data. The documented headless contract itself is an envelope whose
`response` field is textual.

## 3. Mechanisms and tradeoffs

| Product | Headless trigger | Machine-readable transport | Final structured-result control | What remains separate |
| --- | --- | --- | --- | --- |
| Codex CLI | `codex exec` | `--json` JSONL events | `--output-schema FILE` | Event stream shape and schema-enforcement semantics are not specified by the help text. |
| Claude Code | `-p/--print` | `--output-format json` or `stream-json` | `--json-schema` | Outer session/usage metadata and streaming event records remain part of the CLI contract. |
| Gemini CLI | `-p/--prompt`, or non-TTY headless operation | `--output-format json` or `stream-json` | No reviewed CLI schema option | The documented `response` value is a string; event records are not a final arbitrary object. |

### Transport JSON versus result JSON

**Documented fact:** Codex calls its JSON mode “events” and Claude/Gemini
document streaming JSON as event-oriented output. A JSON parser can therefore
successfully parse a stream while still not knowing which event is the
committed final answer.

**Cross-product synthesis:** a robust headless contract has at least two
layers:

1. a stable transport layer for lifecycle, tool, error, usage, and session
   events; and
2. a final-result layer whose location, success condition, and schema
   validation semantics are explicit.

Conflating the layers makes downstream automation brittle: an event envelope
can be valid JSON even when the final answer is absent, textual, refused, or
not schema-valid.

### Schema declaration versus schema guarantee

**Documented fact:** Claude labels its option “structured output validation,”
while Codex's help says the schema describes the final response shape. These
are stronger signals than a prompt instruction, but they are not identical
wording.

**Cross-product synthesis:** documentation should state at least whether the
schema is checked by the CLI, by a provider, or by both; whether invalid output
is retried; and what the caller receives on failure. Without these statements,
“supports JSON Schema” is an underspecified product claim.

## 4. Cross-product synthesis

- **Convergence:** all three products recognize headless automation as a
  distinct mode and offer machine-readable output in some form.
- **Divergence:** Claude is the clearest reviewed example of a named,
  schema-shaped final result (`structured_output`). Codex exposes a similarly
  scoped schema input but its public help is less explicit about the returned
  envelope. Gemini emphasizes a fixed response/usage/error envelope and event
  streaming rather than a caller-defined final schema.
- **Applicability boundary:** a fixed envelope is useful for generic tooling;
  caller-defined schemas are useful when a downstream job needs typed domain
  data. They solve different compatibility problems and should not be judged
  by whether the top-level stdout is JSON.
- **Operational implication (synthesis, not a product fact):** automation needs
  a documented commit point for the final result, a distinct error channel or
  status signal, and a way to distinguish schema failure from model refusal,
  cancellation, transport failure, and ordinary textual output.

## 5. Pitfalls and evidence gaps

1. **Version drift.** The exact help output is version-specific. The observations
   above are for Codex 0.146.0, Claude Code 2.1.212, and Gemini CLI 0.44.1 on
   the research date.
2. **Negative evidence is bounded.** Gemini's lack of a schema option is a
   finding about the reviewed public surface, not a universal claim about
   Google's underlying model APIs or future releases.
3. **Schema dialect and keyword support.** None of the reviewed CLI surfaces
   fully specifies the JSON-Schema draft, annotations, `$ref` behavior, or
   maximum schema size in the evidence collected here.
4. **Failure semantics.** The reviewed sources do not provide one common
   contract for malformed schemas, model refusals, partial streams, retries,
   cancellation, or process exit codes. Claude documents validation failure,
   but that does not automatically transfer to Codex or Gemini.
5. **Streaming versus final output.** A JSONL event stream is not itself a
   structured final result. Consumers need product-specific event semantics or
   a separate final-result channel.
6. **Undisclosed internals.** The sources do not establish whether a product
   applies schema constraints in the provider request, validates after the
   model response, repairs output, or uses more than one mechanism. Treating
   any of those as fact would exceed the evidence.

## References

All sources below were accessed 2026-08-04.

1. OpenAI, *Codex CLI reference*,
   https://developers.openai.com/codex/cli/reference/ . Exact option wording
   additionally recorded from the official `codex-cli 0.146.0` binary with
   `codex exec --help` on the access date.
2. Anthropic, *Claude Code: Headless mode*,
   https://code.claude.com/docs/en/headless.md .
3. Anthropic, *Claude Code CLI reference*,
   https://code.claude.com/docs/en/cli-reference.md . Exact option wording
   additionally recorded from the official `Claude Code 2.1.212` binary with
   `claude --help` on the access date.
4. Google, *Gemini CLI: Headless mode reference*,
   https://geminicli.com/docs/cli/headless/ .
5. Google, *Gemini CLI 0.44.1* shipped help, recorded with `gemini --help` on
   2026-08-04; the official page above is the public behavioral reference.
