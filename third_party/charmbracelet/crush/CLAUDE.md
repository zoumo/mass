# third_party/charmbracelet/crush

Vendored subset of internal packages from [github.com/charmbracelet/crush](https://github.com/charmbracelet/crush).

- **Source**: github.com/charmbracelet/crush
- **Commit**: `e23ef333aa7b58aed57a0e238cd72a908eb1e20d`
- **License**: FSL-1.1-MIT (see [LICENSE.md](LICENSE.md))
- **Go version**: 1.26.1 (required by crush for `new(expr)` syntax and `sync.WaitGroup.Go`)

## Copied directories

| Directory | Source | Modifications |
|-----------|--------|---------------|
| `ansiext/` | `internal/ansiext/` | Import path only |
| `csync/` | `internal/csync/` | Import path only (4 files + doc.go) |
| `stringext/` | `internal/stringext/` | None (no crush-internal imports) |
| `ui/anim/` | `internal/ui/anim/` | Import path only |
| `ui/common/` | `internal/ui/common/` | Import path + simplified `Common` struct (see below) |
| `ui/diffview/` | `internal/ui/diffview/` | Import path only (test files excluded) |
| `ui/list/` | `internal/ui/list/` | Import path only |
| `ui/styles/` | `internal/ui/styles/` | Import path only |
| `ui/util/` | `internal/ui/util/` | Import path only |

## Modification policy

**Third-party code should be kept as close to upstream as possible.** The only acceptable changes are:

1. **Import path rewriting**: `github.com/charmbracelet/crush/internal/...` → `github.com/zoumo/oar/third_party/charmbracelet/crush/...`
2. **Removing test files**: `*_test.go` files are not copied

## Notable modifications

### common.go simplification

The original `Common` struct depends on `crush/internal/config`, `crush/internal/home`,
and `crush/internal/workspace` which are crush business-layer types. The struct has been
simplified to only contain:

- `Styles *styles.Styles`
- `Width int`
- `Height int`

Functions that depended on config/home/workspace have been removed. `DefaultCommon()` takes
no arguments. `PrettyPath` renders the raw path without home-directory shortening.

### Import path rewriting

All `github.com/charmbracelet/crush/internal/...` imports have been rewritten to
`github.com/zoumo/oar/third_party/charmbracelet/crush/...`.
