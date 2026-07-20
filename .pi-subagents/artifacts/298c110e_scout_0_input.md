# Task for scout

Scan the Go source files in /home/ladis/.paseo/worktrees/33e25643/rare-seahorse for debug-level logging statements that should be removed before production. Look for:

1. `log.Printf` / `log.Println` / `log.Print` calls that look like debug/development logging (not production-critical errors or info)
2. Any `fmt.Println`, `fmt.Printf` used for debugging
3. Comments like "// DEBUG" or "// TODO: remove log"
4. Verbose logging that would spam in production (e.g., logging every frame, every connection attempt detail, every resolver step with emoji, etc.)

Focus on files under `internal/` and `cmd/`. Skip third-party/vendor code.

For each finding, report:
- File path and line number
- The exact log statement
- A brief reason why it should be removed (e.g., "debug spam", "dev-only output", "emoji debug logging")

Be thorough — check all .go files.

---
**Output:**
Write your findings to exactly this path: /home/ladis/.paseo/worktrees/33e25643/rare-seahorse/.pi-subagents/artifacts/outputs/298c110e/context.md
This path is authoritative for this run.
Ignore any other output filename or output path mentioned elsewhere, including output destinations in the base agent prompt, system prompt, or task instructions.

## Acceptance Contract
Acceptance level: attested
Completion is not accepted from prose alone. End with a structured acceptance report.

Criteria:
- criterion-1: Return concrete findings with file paths and severity when applicable

Required evidence: review-findings, residual-risks

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