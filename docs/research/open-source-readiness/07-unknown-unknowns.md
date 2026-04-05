# Unknown Unknowns: Open Source Readiness

**Date:** 2026-04-04
**Original researcher:** rapids
**Status:** Reconstructed from summary — needs agent validation/expansion

## Summary

Meta-research covering gaps not addressed by the other 6 research tracks.

## Findings

### CRITICAL: Name Conflict
- "dendra" conflicts with Dendra Systems (dendra.io, $28M funded)
- **Status:** Naming research completed separately. See naming-all-candidates.md.

### CRITICAL: Go Module Path
- Current: `github.com/dmotles/dendra`
- Must change to target org/name before first public release
- Hard to change after people start importing it

### HIGH: Claude Code Hard Dependency
- Tool requires Claude Code CLI (which requires Anthropic API access)
- opus[1m] model referenced in prompts — specific model tier
- API costs are non-trivial for running agent swarms
- **Action:** Prominent documentation of prerequisites and expected costs

### HIGH: Internal References
- "Qumulo-dmotles" Linear team name throughout CLAUDE.md and skills
- "QUM" prefix in issue references
- **Action:** Move to CLAUDE.local.md, .gitignore

### HIGH: No CI/CD Pipeline
- No GitHub Actions workflows exist
- Need at minimum: build + test on PR, release on tag
- **Action:** Set up as part of release mechanism work

### HIGH: Missing Community Files
- No CONTRIBUTING.md
- No CODE_OF_CONDUCT.md
- No SECURITY.md
- **Action:** Create before publish (CONTRIBUTING.md most important)

### MEDIUM: Beads Integration
- References to "Beads" (an AI context-sharing tool) in the codebase
- Optional integration but may confuse new users
- **Action:** Document as optional, or remove references

### MEDIUM: README Needs Overhaul
- Missing badges (license, build status, release version)
- No demo/screencast
- No cost/prerequisites documentation
- No quick-start guide for new users
- **Action:** Major README rewrite as part of launch prep

### MEDIUM: .dendra/ Directory
- Contains agent state, logs, prompts — runtime artifacts
- In the repo because this project develops itself inside dendra
- New users cloning the repo will be confused by `.dendra/` contents
- **Action:** .gitignore `.dendra/` for the public repo, or clean it out

### Other Considerations
- GitHub repo settings (branch protection, issue templates)
- Whether CLAUDE.md and agent prompt files reveal architecture in a problematic way
- Onboarding experience for first-time users
