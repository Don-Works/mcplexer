# MCP Spec Conformance Coverage Matrix

**Spec version audited:** 2025-11-25  
**Date:** 2026-06-13  
**Scope:** Gateway (downstream transports + Code Mode + browser event surface)

## Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Implemented and tested |
| ⚠️  | Partially implemented (gap noted) |
| ❌ | Not implemented |
| 📌 | Pinned by test (regression-guarded) |

---

## 1. Transports

| Feature | Spec ref | Status | Test | Notes |
|---------|----------|--------|------|-------|
| **stdio** | basic/transports | ✅ | `instance_test.go`, `instance_lifecycle_test.go` | Full lifecycle: initialize → initialized → request/response → notifications interleaved |
| **Streamable HTTP — JSON response** | basic/transports | ✅ 📌 | `TestHTTPInstance_RoutesSSEAndJSON` | `Content-Type: application/json` parsed correctly |
| **Streamable HTTP — SSE response** | basic/transfers | ✅ 📌 | `TestReadSSEResponse_NotificationsForwarded` | Parses first matching result from `text/event-stream`; interleaved notifications are forwarded with params |
| **Accept header** | basic/transports | ✅ 📌 | `TestHTTPInstance_AcceptHeaderConformance` | Sends `Accept: application/json, text/event-stream` |
| **Mcp-Session-Id** | basic/transports | ✅ 📌 | `TestHTTPInstance_SessionIDPropagated` | Captured on initialize, echoed on subsequent requests |
| **Initialize handshake** | basic/lifecycle | ✅ 📌 | `TestHTTPInstance_InitializeProtocolVersion` | Sends `initialize` + `notifications/initialized`; uses protocolVersion `2025-03-26` (not latest `2025-11-25`) |
| **401 auth handling** | — | ✅ 📌 | `TestHTTPInstance_AuthRequired` | Returns `ErrAuthRequired`; manager evicts + retries once |
| **SSE — multiple results** | — | ⚠️ 📌 | `TestReadSSEResponse_OnlyFirstResult` | Only first result returned; subsequent results unread |
| **SSE — empty stream** | — | ✅ 📌 | `TestReadSSEResponse_EmptyStreamReturnsError` | Returns error when no result found |
| **SSE — RPC error** | — | ✅ 📌 | `TestReadSSEResponse_RPCErrorPropagated` | Error event surfaced as Go error |

---

## 2. Lifecycle

| Feature | Spec ref | Status | Test | Notes |
|---------|----------|--------|------|-------|
| **initialize request** | basic/lifecycle | ✅ 📌 | `TestHTTPInstance_InitializeProtocolVersion` | Sent on start for both stdio and HTTP |
| **initialized notification** | basic/lifecycle | ✅ 📌 | `TestHTTPInstance_InitializedNotificationSent` | Sent after initialize response |
| **Shutdown** | basic/lifecycle | ✅ | `manager_test.go` | `Manager.Shutdown` stops all instances |

---

## 3. Server Capabilities (Gateway → Client advertisement)

| Capability | Spec ref | Status | Test | Notes |
|------------|----------|--------|------|-------|
| **tools.listChanged** | server/tools | ✅ | `protocol.go:80` | Advertised; `notifications/tools/list_changed` sent on catalog change |
| **resources** | server/resources | ❌ | — | Not advertised; no `resources/list`, `resources/read`, `resources/subscribe` |
| **prompts** | server/prompts | ❌ | — | Not advertised; no `prompts/list`, `prompts/get` |
| **completion** | server/utilities | ❌ | — | Not advertised; no `completion/complete` |
| **logging** | server/utilities | ❌ | — | Not advertised; no `notifications/message` emission |
| **sampling** | server/utilities | ❌ | — | Not advertised; no `sampling/createMessage` |
| **elicitation** | server/utilities | ❌ | — | Not advertised; no `elicitation/create` |

---

## 4. Downstream Notification Handling

| Notification | Spec ref | Status | Test | Notes |
|-------------|----------|--------|------|-------|
| **notifications/tools/list_changed** | server/tools | ✅ 📌 | `TestHandleDownstreamNotify_ToolsListChanged` | Triggers `NotifyToolsChanged` fan-out to live sessions |
| **notifications/resources/list_changed** | server/resources | ⚠️ 📌 | `TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout` | Journaled/logged; no resource cache fan-out yet |
| **notifications/resources/updated** | server/resources | ⚠️ 📌 | `TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout` | Journaled/logged; no resource cache fan-out yet |
| **notifications/prompts/list_changed** | server/prompts | ⚠️ 📌 | `TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout` | Journaled/logged; no prompt cache fan-out yet |
| **notifications/progress** | basic/utilities/progress | ✅ 📌 | `TestMCPConformanceForwardNotification_PreservesParams`, `TestReadResponse_InterleavedNotifications` | Forwarded with params and recorded in downstream event journal |
| **notifications/message** (logging) | server/utilities/logging | ✅ 📌 | `TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout` | Forwarded with params and recorded in downstream event journal |
| **notifications/cancelled** | basic/utilities/cancellation | ✅ 📌 | `TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout` | Forwarded with params and recorded in downstream event journal |

---

## 5. Notification Param Preservation

| Transport | What happens to params? | Status | Test |
|-----------|------------------------|--------|------|
| **stdio** | `forwardNotification` extracts `method` and `params`; calls `onNotify(method, params)` | ✅ 📌 | `TestMCPConformanceForwardNotification_PreservesParams` |
| **HTTP SSE** | `readSSEResponse` forwards notification method + params before returning the matching response | ✅ 📌 | `TestReadSSEResponse_NotificationsForwarded` |
| **Manager routing** | `handleDownstreamNotify(key, method, params)` appends every notification to the bounded event journal | ✅ 📌 | `TestHandleDownstreamNotify_OtherMethodsJournaledNoToolsFanout` |

**Remaining gap:** resources/prompts/logging are journaled for agents, but the gateway still does not advertise full resources/prompts/logging capabilities or maintain resource/prompt caches.

---

## 6. Code Mode (mcpx__execute_code) Contract

| Feature | Status | Test | Notes |
|---------|--------|------|-------|
| **Synchronous tool calls** | ✅ 📌 | `TestToolCaller_SingleResultContract` | `ToolCaller.CallTool` returns `(json.RawMessage, error)` — single result, no streaming |
| **ExecutionResult is snapshot** | ✅ 📌 | `TestExecutionResult_IsCompleteSnapshot` | All tool calls recorded after execution; no incremental/partial results |
| **Multiple tool calls captured** | ✅ 📌 | `TestExecutionResult_MultipleToolCalls` | Sequential and parallel calls all in snapshot |
| **Error propagation synchronous** | ✅ 📌 | `TestExecutionResult_ErrorPropagation` | Tool errors throw JS exceptions synchronously |
| **No streaming fields** | ✅ 📌 | `TestExecutionResult_NoStreamingFields` | No progress/partial/stream/delta/incremental fields in result JSON |
| **Progress notifications from tools** | ❌ | — | No mechanism for downstream tool to send progress to Code Mode |
| **Logging from tools** | ❌ | — | No mechanism for downstream tool logging to reach Code Mode output |
| **Partial/streaming tool results** | ❌ | — | Tool must return complete result before Code Mode continues |

---

## 7. Response ID Matching (Desync Detection)

| Feature | Status | Test | Notes |
|---------|--------|------|-------|
| **Numeric ID match** | ✅ 📌 | `TestResponseIDMatches_Numeric` | Standard JSON-RPC numeric IDs |
| **String-encoded ID tolerance** | ✅ 📌 | `TestResponseIDMatches_StringEncoded` | Spec-loose servers that echo `"5"` instead of `5` |
| **Mismatch → desync error** | ✅ | `instance.go:316` | `ErrResponseDesync` triggers eviction |

---

## 8. Protocol Version

| Area | Current | Latest Spec | Gap |
|------|---------|-------------|-----|
| **Gateway init (downstream)** | `2025-03-26` | `2025-11-25` | Gateway advertises older protocol version to downstreams |
| **Gateway init (upstream/client)** | `2025-03-26` | `2025-11-25` | Clients connecting to the gateway see the older version |

---

## 9. Browser Event Surface

| Feature | Status | Notes |
|---------|--------|-------|
| **Event journal** | ✅ | Bounded per-downstream event journal records notifications, progress, logging, and cancellation |
| **Delta subscription** | ✅ | `downstream_events_since` / `downstream_events_batch` expose cursor-based deltas |
| **Wait/poll tools** | ✅ | `downstream_events_wait` lets Code Mode block for async downstream events without streaming tool results |
| **Live SSE to browser** | ❌ | Dashboard uses polling, not SSE push for downstream events |

---

## 10. Recommended Gap-Closure Priorities

1. **Protocol version bump** — Update `2025-03-26` → `2025-11-25` after verifying compatibility. Low risk.
2. **Server capability expansion** — Advertise resources/prompts/logging when implemented. Low risk (additive).
3. **Browser live event stream** — Add an SSE or websocket dashboard channel for downstream journal deltas. Medium risk.
4. **Resource/prompt cache routing** — Turn journaled resources/prompts notifications into cache invalidation once those capabilities exist.

**Non-goal:** Model-streamed tool results (streaming `tools/call` results). The synchronous Code Mode contract should be preserved; async events should be surfaced via the journal/wait mechanism, not by changing the tool result contract.
