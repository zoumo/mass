# third_party/charmbracelet/crush

Vendored subset of internal packages from [github.com/charmbracelet/crush](https://github.com/charmbracelet/crush).

- **Source**: github.com/charmbracelet/crush
- **Commit**: e23ef333aa7b58aed57a0e238cd72a908eb1e20d
- **License**: FSL-1.1-MIT (see [LICENSE.md](LICENSE.md))

## Copied directories

| Directory | Source | Modifications |
|-----------|--------|---------------|
| `csync/` | `internal/csync/` | Direct copy (4 files + doc.go), no crush-internal imports |
| `stringext/` | `internal/stringext/` | Direct copy, no crush-internal imports |
| `ui/util/` | `internal/ui/util/` | Direct copy, no crush-internal imports |
| `ui/styles/` | `internal/ui/styles/` | Removed `diffview` import; replaced `diffview.Style`/`diffview.LineStyle` with inline `DiffStyle`/`DiffLineStyle` types; replaced Go 1.26 `new(expr)` calls with `ptr(expr)` helper for Go 1.25 compat |
| `ui/anim/` | `internal/ui/anim/` | Changed `crush/internal/csync` import to our third_party path |
| `ui/common/` | `internal/ui/common/` | Changed crush-internal imports; simplified `Common` struct (see below); removed `home.Short` dependency in `PrettyPath` |
| `ui/list/` | `internal/ui/list/` (partial, pre-existing) | Updated `highlight.go` to use `stringext.NormalizeSpace` instead of inline implementation |

## Notable modifications

### diffview not ported

The `internal/ui/diffview` package is not included. The `Diff` field on `styles.Styles`
uses inline `DiffStyle`/`DiffLineStyle` types that mirror the original `diffview.Style`/`diffview.LineStyle`
struct layout.

### common.go simplification

The original `Common` struct depends on `crush/internal/config`, `crush/internal/home`,
and `crush/internal/workspace` which are crush business-layer types. The struct has been
simplified to only contain:

- `Styles *styles.Styles`
- `Width int`
- `Height int`

Functions that depended on config/home/workspace have been removed. `DefaultCommon()` takes
no arguments (the original took a `workspace.Workspace`). `PrettyPath` renders the raw path
without home-directory shortening.

### Import path rewriting

All `github.com/charmbracelet/crush/internal/...` imports have been rewritten to
`github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/...`.
