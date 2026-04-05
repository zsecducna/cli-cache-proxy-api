# Claude Code To GPT Routing Design

## Summary

Design a strict compatibility path that lets Claude Code call arbitrary user-configured `gpt-*` model IDs through the proxy without changing Claude Code itself.

Claude Code will continue talking to the proxy using Anthropic Messages-compatible requests. When the inbound model string starts with `gpt-`, the proxy will route the request to an OpenAI-compatible backend instead of a Claude backend, translate the request shape, validate strict compatibility before execution, and translate the response back into Claude-compatible output.

The v1 goal is not "make every Claude feature work on GPT". The v1 goal is "make the safe subset work reliably, and reject everything else clearly before upstream execution."

## Goals

- Keep Claude Code client behavior unchanged.
- Accept arbitrary configured `gpt-*` model strings without a proxy allowlist.
- Support any OpenAI-compatible backend the proxy already supports.
- Fail fast when a Claude request cannot be represented faithfully on the selected GPT backend.
- Reuse existing translator and executor paths where possible.

## Non-Goals

- No changes to Claude Code UX, picker behavior, or required client-side wrapper scripts.
- No silent degradation of Claude-only features.
- No provider-specific GPT allowlist in proxy code.
- No promise that every OpenAI-compatible backend supports every `gpt-*` model.
- No full semantic equivalence for Claude-only behaviors in v1.

## User Experience

The user continues to point Claude Code at the proxy as an Anthropic-compatible gateway.

The user may configure a `gpt-*` model ID in Claude Code through its normal model configuration surface, such as environment variables or custom model options. Claude Code still sends Anthropic Messages-compatible requests to the proxy.

If the request is compatible and a matching OpenAI-compatible backend exists, the request succeeds and the response is returned in Claude-compatible format.

If the request is incompatible with GPT routing, the proxy rejects it locally with a clear structured error that names the unsupported feature or routing failure.

## Architecture

### Ingress Contract

Claude Code continues calling the existing Anthropic Messages-compatible endpoint. This remains compliant with Anthropic's LLM gateway contract, including forwarding required Anthropic headers.

The request source format remains `claude`.

### Route Classification

Add a route-classification step after handler request parsing and before provider selection:

- If inbound source format is `claude` and the trimmed requested model starts with the lowercase prefix `gpt-`, mark the request as `claude_via_openai_compat`.
- Otherwise preserve current routing behavior.

This classification should be stored in request execution metadata, not inferred repeatedly in downstream layers.

### Execution Path

For `claude_via_openai_compat` requests:

1. Run syntax-level compatibility validation that is independent of backend choice.
2. Select only OpenAI-compatible providers and auths.
3. Resolve backend surface capabilities for the selected route candidates.
4. Run backend-specific compatibility validation.
5. Prefer OpenAI Responses execution if the backend supports it.
6. Fall back to Chat Completions only if the request fits the stricter Chat-Completions-safe subset.
7. Translate the upstream response back to Claude-compatible output.

For ordinary Claude requests:

- Keep using the current Claude executor path.

## Components

### 1. Request Route Classifier

Responsibility:

- classify Claude ingress requests as native-Claude or Claude-via-GPT
- attach route metadata for downstream selection

Interface:

- input: `handlerType`, requested model string, request metadata
- output: route class enum plus trimmed raw requested model

Normalization rules:

- trim leading and trailing whitespace
- preserve original case and internal characters
- do not rewrite, alias, or case-normalize arbitrary `gpt-*` identifiers in v1
- route classification uses the trimmed raw string and performs a case-sensitive match on lowercase `gpt-`

Why separate:

- avoids duplicated `gpt-*` checks across handler, selector, and executor code
- makes tests deterministic and cheap

### 2. Claude-Via-GPT Compatibility Validator

Responsibility:

- enforce strict pre-upstream validation
- reject unsupported Claude-only features before execution

Validation policy uses two passes:

- Syntax pass validates request features without considering a specific backend.
- Backend pass validates the already accepted request against the chosen backend surface.
- V1 only supports a strict subset of Claude Messages requests.
- unsupported semantics must return an error, not be approximated

Syntax-pass supported subset:

- system text blocks only
- user text messages only
- assistant text messages only
- standard tools with:
  - non-empty string `name`
  - optional string `description`
  - `input_schema` as a JSON object
- assistant `tool_use` blocks with:
  - non-empty string `id`
  - non-empty string `name`
  - `input` as a JSON object
- user `tool_result` blocks with:
  - non-empty string `tool_use_id`
  - content limited to plain text or a JSON object that can be serialized deterministically
- streaming required
- `max_tokens`
- `temperature`
- `top_p`
- stop sequences

V1 explicit exclusions:

- non-stream Claude requests
- explicit Claude thinking or adaptive reasoning controls

Syntax-pass rejection candidates:

- image, audio, document, or other non-text content blocks
- tool schemas that are not JSON objects
- tool-use payloads whose `input` is not a JSON object
- tool-result payloads that are not text or JSON objects
- assistant messages that mix text content blocks and `tool_use` blocks in the same message
- turns with empty content arrays
- multiple `tool_result` blocks referencing the same `tool_use_id`
- `tool_result` blocks that do not reference a `tool_use` from the immediately preceding assistant turn
- assistant tool-use turns whose `tool_use` ids are not unique within the turn
- request structures that require semantic approximation
- Claude beta features with no defined GPT path

Backend-pass requirements:

- backend must support streaming, or request is rejected
- backend must support either Responses or the narrower Chat-Completions-safe subset
- backend must support tools when tool definitions or tool-use/tool-result blocks are present
- tool-result JSON objects must be serialized canonically with stable key ordering before translation for deterministic downstream output and stable test assertions only
- tool turns must satisfy the v1 transcript invariant:
  - one assistant turn may contain one or more `tool_use` blocks and no text content blocks
  - the immediately following user turn may contain one or more `tool_result` blocks and no text content blocks
  - every `tool_result.tool_use_id` must map to exactly one `tool_use.id` from the immediately preceding assistant turn
  - result order must match the order of the referenced `tool_use` blocks
  - every `tool_use` in the assistant turn must have exactly one matching `tool_result` in the following user turn

Chat-Completions-safe subset:

- text-only messages
- no reasoning-specific controls
- no tool use
- no request features that require Responses-only semantics

Why separate:

- keeps the translator focused on shape conversion rather than business-policy rejection
- makes strict-fail behavior visible and testable

Interface:

- input: route-classified Claude request plus optional backend capability record
- output: `accepted` or structured compatibility error with machine-readable reason code

### 3. Backend Capability Resolver

Responsibility:

- filter candidate auths and providers for Claude-via-GPT requests
- know which backends support Responses, streaming, tools, reasoning controls, and model families

Minimum capability fields:

- `supports_openai_responses`
- `supports_chat_completions`
- `supports_tools`
- `supports_streaming`

Behavior:

- only OpenAI-compatible executors are eligible
- Claude, Codex ChatGPT backend, Gemini, and other non-OpenAI-compatible providers are excluded
- if no backend remains after capability filtering, return a local routing error
- if one or more eligible backends remain, selection follows the existing auth-manager provider/auth ordering and selector rules
- authoritative model-support sources in v1 are:
  - explicit auth/backend model allowlists or deny rules when configured
  - explicit upstream `model unsupported` responses observed during candidate attempts
- `claude_via_gpt_model_not_supported` means at least one otherwise healthy OpenAI-compatible backend was considered and every candidate was rejected by configured model-support rules or explicit upstream `model unsupported` responses
- `claude_via_gpt_backend_not_available` means no eligible OpenAI-compatible backend/auth remained after capability filtering, disablement, or availability checks, or model support was still unknown for every candidate

Why separate:

- preserves the requirement that arbitrary `gpt-*` strings are accepted immediately

Interface:

- input: requested model, route class, auth/provider candidates
- output: filtered backend candidates with capability records

V1 capability source:

- static provider/executor knowledge
- explicit auth/backend attributes

Negative capability learning is deferred from the mandatory v1 path.

### 4. Claude-To-OpenAI Translator Surface

Responsibility:

- translate Claude-format request bodies into OpenAI-family request bodies

V1 reuse plan:

- reuse the existing Claude -> OpenAI translation path for compatible fields
- keep the source format as `claude`
- add explicit guardrails so translation only runs after strict validation

Target execution preference:

- Responses first
- Chat Completions second, but only for the narrower safe subset

Longer-term follow-up:

- add a dedicated Claude -> OpenAI Responses translator if current translator behavior is too Chat-Completions-shaped for GPT-5 workloads

Interface:

- input: syntax-valid Claude request, selected backend surface
- output: OpenAI Responses request or OpenAI Chat Completions request
- failure: structured translation error when the selected surface cannot represent the request faithfully

### 5. Response Re-encoding Layer

Responsibility:

- return Claude-compatible responses to Claude Code regardless of which OpenAI-compatible backend served the request

Requirements:

- preserve tool-call/result sequencing
- preserve streaming semantics that Claude Code expects
- make backend/provider mismatch invisible unless an explicit compatibility failure occurs

V1 rule:

- streaming is mandatory for Claude-via-GPT routing
- if the selected backend cannot stream in a way the proxy can re-encode to Claude-compatible streaming output, the request must be rejected before execution
- successful non-stream execution is out of scope in v1; inbound non-stream Claude requests are rejected before backend selection

Interface:

- input: backend stream, selected surface, original Claude request
- output: Claude-compatible stream
- success criteria:
  - event ordering remains valid for Claude Code consumption
  - tool call order, tool result order, and tool/result pairing are preserved
  - no response-shape downgrade is hidden from the client
  - successful Claude-compatible terminal output must include a stable final message shape for the supported subset
  - `stop_reason` must be synthesized from upstream terminal metadata via a fixed mapping table
  - usage/token accounting is emitted only from upstream fields that actually exist

Stop-reason mapping table for v1:

- upstream terminal reason `stop` -> Claude `end_turn`
- upstream terminal reason `length` -> Claude `max_tokens`
- upstream terminal reason `tool_calls` or `function_call` -> Claude `tool_use`
- missing or unknown upstream terminal reason -> reject translation unless the response can still be emitted as a non-terminal error-free partial stream

## Data Flow

1. Claude Code sends Anthropic Messages request to the proxy.
2. Handler extracts requested model and source format.
3. Route classifier marks request as native-Claude or Claude-via-GPT.
4. If Claude-via-GPT:
   - run syntax-pass compatibility validator
   - run capability-aware auth/backend filtering
   - run backend-pass compatibility validator
   - choose Responses or Chat Completions path
   - translate request to OpenAI-family payload
   - execute against selected OpenAI-compatible backend
   - translate response back to Claude-compatible output
5. Return Claude-compatible response to Claude Code.

If any stage fails, return a local structured error without calling upstream.

## Error Handling

Errors must be explicit and categorized. Do not collapse them into generic provider errors.

Required classes:

- `claude_via_gpt_incompatible_request`
- `claude_via_gpt_backend_not_available`
- `claude_via_gpt_model_not_supported`
- `claude_via_gpt_surface_not_supported`
- `claude_via_gpt_streaming_not_supported`
- `claude_via_gpt_translation_failed`

Error messages should identify:

- requested model
- selected route class
- failing stage
- precise incompatibility when known

Example failure wording:

- "Claude-via-GPT routing rejected: request uses unsupported Claude content block `input_audio`."
- "Claude-via-GPT routing rejected: no OpenAI-compatible backend supports model `gpt-5.4-custom`."
- "Claude-via-GPT routing rejected: backend only supports Chat Completions but request requires Responses-compatible tool semantics."

Outbound client contract:

- non-streaming rejections return an Anthropic-compatible JSON error body with a 4xx status
- streaming rejections that occur before upstream execution return a single Anthropic-compatible SSE `error` event and close the stream
- streaming failures after upstream execution begins return a single Anthropic-compatible SSE `error` event if no Claude-compatible success payload has yet been emitted; otherwise the stream closes and logs the failure server-side

Minimum rejection payload fields:

- `type`
- `error.type`
- `error.message`
- `request_id`

Status mapping:

- incompatible request: `400`
- model not supported: `400`
- surface not supported: `400`
- streaming not supported: `400`
- backend not available: `503`
- translation failed: `500`

Streaming rejection shape:

- emit `event: error`
- emit `data: {"type":"error","error":{"type":"invalid_request_error","message":"..."}}` for pre-upstream validation failures
- include `request_id` when available
- terminate the stream without upstream connection establishment
- no partial success events may precede the rejection payload

Fallback behavior by stage:

- syntax-pass validation failure: fail immediately, do not try another backend
- capability filtering yields zero candidates: return `claude_via_gpt_backend_not_available`
- backend-pass validation failure for a candidate surface: try the next eligible candidate if one exists, otherwise return `claude_via_gpt_surface_not_supported`
- translation failure: fail immediately, do not try another backend
- explicit upstream `model unsupported` from a selected backend: try the next eligible candidate; if every eligible candidate is explicitly unsupported by configured rules or upstream unsupported responses, return `claude_via_gpt_model_not_supported`
- stream-start failure before any success event: try the next eligible candidate if one exists, otherwise return `claude_via_gpt_backend_not_available`
- after the first Claude-compatible success event has been emitted to the client, no backend retry is allowed
- after the first Claude-compatible success event has been emitted to the client, no backend retry is allowed

## Testing Strategy

### Unit Tests

- route classification for Claude + `gpt-*`
- syntax-pass validator acceptance for supported subset
- syntax-pass validator rejection for unsupported Claude-only features
- backend-pass validator acceptance and rejection by surface capability
- capability filtering by provider type and backend feature flags

### Translator Tests

- Claude request -> OpenAI-family request for supported subset
- single-tool and multi-tool sequencing with deterministic pairing
- rejection coverage for non-representable structures

### Executor / Integration Tests

- Claude ingress with `gpt-*` routes to OpenAI-compatible executor path
- arbitrary `gpt-*` strings are accepted by the proxy before backend resolution
- no eligible backend yields local routing error
- Responses-capable backend path succeeds
- Chat-Completions-only backend succeeds only for the safe subset
- unsupported requests fail before upstream execution
- response translation returns Claude-compatible shapes to the client

Concrete end-to-end fixtures required:

- plain text streaming turn over Responses-capable backend
- multi-tool assistant turn with matching multi-tool result turn over Responses-capable backend
- pre-upstream rejection turn that returns Anthropic-compatible error output
- Chat-Completions-safe request that is accepted by the fallback path
- request rejected because it exceeds the Chat-Completions-safe subset on a Chat-Completions-only backend
- multi-candidate route where the first backend returns explicit model unsupported and the second backend succeeds
- post-stream-start upstream failure after one emitted success event, proving no retry occurs

## Rollout Plan

### Phase 1

Phase 1 is the only planning target for the first implementation plan.

- Add route classification
- Add syntax-pass and backend-pass compatibility validators
- Add backend capability resolver
- Add Claude-via-GPT execution path using existing translator plus OpenAI-compatible executor
- Require streaming-capable backend surfaces
- Limit execution to Responses-first, Chat-Completions-safe-fallback

### Phase 2

- Add richer backend capability discovery and persistence
- Add optional negative capability learning
- Improve error diagnostics and observability
- Add dedicated Claude -> OpenAI Responses translation if needed

### Phase 3

- Expand supported subset only when semantics can be preserved and tested

## Observability

Add request-side observability fields for Claude-via-GPT requests:

- route class
- selected provider
- selected backend surface (`responses` or `chat_completions`)
- validation pass/fail reason
- capability filter reason when a backend is skipped

This should make real Claude Code failures actionable without reading raw upstream logs.

## V1 Decisions

- V1 capability metadata comes from static provider/executor knowledge plus explicit auth/backend attributes. Runtime negative-cache learning is allowed as a supplement, but active probing is not required in v1.
- Explicit upstream `model unsupported` responses are authoritative for Phase 1 model-not-supported classification during candidate attempts.
- V1 supports text messages plus a narrow, explicit tool-use/tool-result subset as defined in the compatibility validator.
- Image, audio, document, and other non-text content blocks are rejected for Claude-via-GPT routing.
- Any tool structure outside the declared subset is rejected locally.
- V1 considers the existing response translation path acceptable only for the approved subset and must be covered by contract tests. If a backend surface fails those tests, that surface is not eligible for Claude-via-GPT routing.
- V1 requires streaming-capable backend surfaces. No silent downgrade from streaming to non-streaming is allowed.
- V1 rejects all explicit Claude thinking or adaptive reasoning controls for Claude-via-GPT routing.
- V1 supports multi-tool turns only under the exact transcript invariant defined in the backend-pass validator.

## Deferred Work

- dedicated Claude -> OpenAI Responses translator for richer GPT-5 semantics
- richer learned backend capability persistence beyond static metadata
- expansion beyond text-only Claude content once translator fidelity is proven with tests
- explicit Claude reasoning/thinking control support
- non-stream Claude-via-GPT execution

## Recommendation

Proceed with a strict, Responses-first Claude-via-GPT route that:

- keeps Claude Code unchanged
- accepts arbitrary configured `gpt-*` strings
- uses only OpenAI-compatible backends
- rejects unsupported Claude semantics before execution
- treats compatibility and capability filtering as first-class units, not side effects hidden inside translators

This is the smallest design that can work reliably without pretending Claude and GPT APIs are semantically identical.

## References

- Anthropic Claude Code LLM gateway requirements: https://docs.anthropic.com/en/docs/claude-code/llm-gateway
- Claude Code model configuration and custom model options: https://code.claude.com/docs/en/model-config
- Anthropic Messages streaming event and error shape: https://docs.anthropic.com/en/docs/build-with-claude/streaming
- Anthropic JSON error shape: https://docs.anthropic.com/en/api/errors
