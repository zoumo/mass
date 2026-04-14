# 开发指南

- use `make build` to build go binary 

## Project Intelligence

Reference files for architecture, decisions, conventions, and design:

| File | Description |
|------|-------------|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System overview, component map, data flow, tech stack |
| [.gsd/DECISIONS.md](.gsd/DECISIONS.md) | All architectural and implementation decisions (D001–D112+) |
| [.gsd/KNOWLEDGE.md](.gsd/KNOWLEDGE.md) | Rules, patterns, and lessons learned (K001–K080+) |
| [docs/design/](docs/design/) | Detailed design documents for each subsystem |

## 设计一致性

- Code changes **must be** aligned with `docs/design`
- No need to consider compatibility Now

## Language-Agnostic Coding Principles

These principles apply to all programming languages and should guide code review and development decisions.

### Clarity

- Code must explain "what" and "why"
- Descriptive variable names over brevity
- Clarity trumps cleverness
- Write code as readable as narrative

### Simplicity

- Prefer standard tools over custom solutions
- Least mechanism principle
- Top-to-bottom readability
- Avoid unnecessary abstraction levels

### Concision

- High signal-to-noise ratio
- Avoid redundant comments (don't repeat code)
- Use common idioms
- Boost important signals with comments

### Maintainability

- Code is modified more than written
- Easy to modify correctly
- Clear assumptions
- No hidden critical details
- Predictable naming patterns
- Minimize dependencies

### Consistency

- Follow existing patterns in codebase
- Local consistency acceptable when not harming readability
- Style deviations not acceptable if they worsen existing issues

### DRY (Don't Repeat Yourself)

- Avoid duplicate logic
- Use abstraction when complexity justifies it

### KISS (Keep It Simple, Stupid)

- Prefer simple solutions
- Avoid premature optimization
