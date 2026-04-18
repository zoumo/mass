# 开发指南

- use `make build` to build go binary 
- use `make fmt` to fix code format
- use `make lint` to find lint error

## Project Intelligence

Reference files for architecture, decisions, conventions, and design:

| File | Description |
|------|-------------|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System overview, component map, data flow, tech stack |
| [.gsd/DECISIONS.md](.gsd/DECISIONS.md) | All architectural and implementation decisions (D001–D112+) |
| [.gsd/KNOWLEDGE.md](.gsd/KNOWLEDGE.md) | Rules, patterns, and lessons learned (K001–K080+) |
| [docs/design/](docs/design/) | Detailed design documents for each subsystem |
| [code-principle](docs/develop/rules/code-principle.md) | **Must** Follow Basic Guidelines for Code Development |

## Skills

| Skill | Path | Description |
|-------|------|-------------|
| massctl | [skills/massctl/SKILL.md](skills/massctl/SKILL.md) | Use MASS to manage workspaces, agent lifecycles, orchestrate multi-agent collaboration |

## 设计一致性

- Code changes **must be** aligned with `docs/design`
- No need to consider compatibility Now

## graphify

This project has a graphify knowledge graph at graphify-out/.

Rules:
- Before answering architecture or codebase questions, read graphify-out/GRAPH_REPORT.md for god nodes and community structure
- If graphify-out/wiki/index.md exists, navigate it instead of reading raw files
- After modifying code files in this session, run `python3 -c "from graphify.watch import _rebuild_code; from pathlib import Path; _rebuild_code(Path('.'))"` to keep the graph current
