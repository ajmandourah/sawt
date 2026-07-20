# Task for planner

You are a delegated subagent running from a fork of the parent session. Treat the inherited conversation as reference-only context, not a live thread to continue. Do not continue or answer prior messages as if they are waiting for a reply. Your sole job is to execute the task below and return a focused result for that task using your tools.

Task:
Create an implementation plan to fix a duplicate URL entry bug in /home/ladis/.paseo/worktrees/33e25643/rare-seahorse.

**BUG SUMMARY:**
- `LoadURLs()` is called TWICE during startup:
  1. Inside `store.New()` via `loadURLs()` (store.go:74) — first call, appends URL tracks to `trackOrder`
  2. Explicitly in `cmd/sawt/main.go:117` — second call, appends the SAME tracks again to `trackOrder`
- Result: after restart, URL tracks appear twice in the library listing (duplicate in `trackOrder`, though `s.tracks` map has only one entry)

**Files involved:**
- `cmd/sawt/main.go` — contains the redundant `apiStore.LoadURLs(...)` call on line 117
- `internal/api/store/store.go` — `LoadURLs()` at line ~391 unconditionally appends to `trackOrder` without checking for duplicates

**Fix needed:**
1. Remove the redundant `LoadURLs` call from `cmd/sawt/main.go:117` (since `store.New()` already calls `loadURLs()` internally)
2. As a safety measure, also add a dedup check in `LoadURLs()` so it never appends a track ID that's already in `trackOrder`

Provide a step-by-step implementation plan with exact changes needed.

---
**Output:**
Write your findings to exactly this path: /home/ladis/.paseo/worktrees/33e25643/rare-seahorse/.pi-subagents/artifacts/outputs/4d9073bb/plan.md
This path is authoritative for this run.
Ignore any other output filename or output path mentioned elsewhere, including output destinations in the base agent prompt, system prompt, or task instructions.

## Acceptance Contract
Acceptance level: checked
Completion is not accepted from prose alone. End with a structured acceptance report.

Criteria:
- criterion-1: Implement the requested change without widening scope

Required evidence: changed-files, tests-added, commands-run, residual-risks, no-staged-files

Finish with a fenced JSON block tagged `acceptance-report` in this shape:
Use empty arrays when no items apply; array fields contain strings unless object entries are shown.
`criteriaSatisfied[].status` must be exactly one of: satisfied, not-satisfied, not-applicable.
`commandsRun[].result` must be exactly one of: passed, failed, not-run.
`manualNotes` and `notes` are optional strings; an empty string means no note and does not satisfy `manual-notes` evidence.
```acceptance-report
{
  "criteriaSatisfied": [
    {
      "id": "criterion-1",
      "status": "satisfied",
      "evidence": "specific proof"
    }
  ],
  "changedFiles": [
    "src/file.ts"
  ],
  "testsAddedOrUpdated": [
    "test/file.test.ts"
  ],
  "commandsRun": [
    {
      "command": "command",
      "result": "passed",
      "summary": "short result"
    }
  ],
  "validationOutput": [
    "validation output or concise summary"
  ],
  "residualRisks": [
    "none"
  ],
  "noStagedFiles": true,
  "diffSummary": "short description of the diff",
  "reviewFindings": [
    "blocker: file.ts:12 - issue found, or no blockers"
  ],
  "manualNotes": "anything else the parent should know"
}
```