# M006: Fix golangci-lint v2 issues

## Vision
Make the codebase fully golangci-lint v2 clean. 202 issues across 11 linter categories — auto-fixable formatter issues handled first, then manual fixes for type-safety and dead code, finishing with test assertion quality.

## Slice Overview
| ID | Slice | Risk | Depends | Done | After this |
|----|-------|------|---------|------|------------|
| S01 | Auto-fix: gci + gofumpt formatting (56 issues) | low | — | ✅ | golangci-lint run ./... shows no gci or gofumpt findings. |
| S02 | Auto-fix: unconvert + copyloopvar + ineffassign (24 issues) | low | — | ✅ | golangci-lint run ./... shows no unconvert / copyloopvar / ineffassign findings. |
| S03 | Manual: misspell + unparam (17 issues) | low | — | ✅ | golangci-lint run ./... shows no misspell or unparam findings. |
| S04 | Manual: unused dead code (12 issues) | medium | — | ✅ | golangci-lint run ./... shows no unused findings. |
| S05 | Manual: errorlint — type assertions on errors (17 issues) | medium | — | ✅ | golangci-lint run ./... shows no errorlint findings. |
| S06 | Manual: gocritic (45 issues) | low | — | ✅ | golangci-lint run ./... shows no gocritic findings. |
| S07 | Manual: testifylint (31 issues) | low | — | ✅ | golangci-lint run ./... reports 0 issues. |
