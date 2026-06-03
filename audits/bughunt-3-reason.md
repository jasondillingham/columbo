# Lane Reason (driven review)

**Lane:** reason (bughunt-3)
**Date:** 2026-06-03
**Baseline:** columbo ``
**Target:** columbo
**Status:** 2 finding(s)

Severity scale:
- **CRITICAL** — exploitable RCE, arbitrary file write, trust bypass
- **HIGH** — privilege boundary breach, DoS, secret leakage, state corruption that survives recovery
- **MEDIUM** — resource exhaustion within bounds, error-swallowing that masks problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage, future-proofing

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F001 | LOW |  | L6 silent-drop grading: any non-JSON stdout line counts as "responded", so a server that logs to stdout while dropping a malformed frame is graded PASS instead of FINDING (masks the silent-drop class) | confirmed |
| F002 | LOW |  | L6 `errorCode` mislabels a code-absent JSON-RPC error as "code:0": an error object with no `code` field is reported as the jsonrpc-code-zero class ("error code 0"), a misdiagnosis | confirmed |

## Contracts re-verified (no findings)

(none recorded)

---

## F001 — LOW — L6 silent-drop grading: any non-JSON stdout line counts as "responded", so a server that logs to stdout while dropping a malformed frame is graded PASS instead of FINDING (masks the silent-drop class)

**Files:**
- `internal/lanes/l6.go`
- `internal/probes/mcp/client.go`

**Expected.** A frame should count as "responded" only when it is an actual JSON-RPC message (carries jsonrpc/result/error, or a null-id error), not when it is an unparseable `_raw` stdout line. A server that logs to stdout and drops the frame must still surface the silent-drop finding.

**Observed.** classifyL6's NotSilent branch calls respondedBeyondHandshake(s), which returns true for the first response frame whose `id` is not float64(1). The mcp client stores any non-JSON stdout line as Frame{"_raw": line}. Such a frame has no `id`, so `r["id"].(float64)` is ok=false and respondedBeyondHandshake returns true. Result: if the target writes ANY non-JSON-RPC line to stdout (a log message) after the handshake while silently dropping the malformed probe frame, the lane grades the probe PASS ("server responded, not silently dropped") instead of emitting the MEDIUM silent-drop finding. The check was written loose on purpose to accept a valid null-id JSON-RPC error frame as a response, but it cannot distinguish that from log noise, so it masks genuine silent drops.

**Reproducer.**

```
go test ./internal/lanes -run TestNoiseMasksSilentDrop
```

**Fix shape.** In respondedBeyondHandshake, ignore `_raw` frames (and require the frame to look like a JSON-RPC response: has "result" or "error", or jsonrpc=="2.0"). Only such a frame, or a genuine null-id error, counts as a response. _raw noise does not.

---

## F002 — LOW — L6 `errorCode` mislabels a code-absent JSON-RPC error as "code:0": an error object with no `code` field is reported as the jsonrpc-code-zero class ("error code 0"), a misdiagnosis

**Files:**
- `internal/lanes/l6.go`

**Expected.** A code-absent error and a literal code:0 are distinct findings. errorCode should signal "no numeric code present" separately from "code == 0" so classifyL6 can report each accurately (e.g. an "error object missing required code" finding vs. the code:0 finding).

**Observed.** errorCode returns (0, true) for an error object that has no numeric `code` field (the `return 0, true // has an error object but no numeric code` branch). classifyL6's NonzeroCode path then treats `code == 0` identically whether the code is genuinely 0 or simply absent, and emits the F004-class finding titled "JSON-RPC error uses code:0" with Observed "error code 0" and Class "jsonrpc-code-zero". But a code-ABSENT error is a different defect (JSON-RPC 2.0 §5.1 requires `code`); reporting it as "code is 0" is a wrong diagnosis. The two conditions (code present and equal to 0 vs. code missing entirely) are conflated under one mislabeled class.

**Reproducer.**

```
go test ./internal/lanes -run TestErrorMissingCodeMislabeledAsZero
```

**Fix shape.** Have errorCode return three states (code value, hasError, hasNumericCode) or a sentinel for "code absent". In classifyL6, branch: absent code -> a "missing required error code" finding; code == 0 -> the existing code:0 finding; otherwise PASS. Don't render "error code 0" for an error that carried no code.
