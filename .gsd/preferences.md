---
version: 1
mode: solo
skill_discovery: auto
skill_staleness_days: 0
uat_dispatch: false
unique_milestone_ids: false
notifications:
  enabled: true
  on_complete: true
  on_error: true
  on_budget: true
  on_milestone: true
  on_attention: true
cmux:
  enabled: true
  notifications: true
  sidebar: true
  splits: false
  browser: false
git:
  auto_push: false
  push_branches: false
  snapshots: true
  pre_merge_check: auto
  merge_strategy: squash
  isolation: branch
phases:
  skip_research: false
  skip_reassess: false
  skip_slice_research: false
  reassess_after_slice: false
post_unit_hooks:
  - name: sync-slice-docs
    after:
      - complete-slice
    prompt: |
      你的任务是从 .gsd/ 的原始数据全量重建两份项目级文档。

      ## 任务 1: docs/DECISIONS.md

      1. 读取 .gsd/DECISIONS.md 全文
      2. 筛选 scope 为 global 或 architecture 的决策，跳过 scope 为 task 或 slice 的条目
      3. 检查 superseded_by 字段：
         - 无 superseded_by → 放入 "Active Decisions" 区
         - 有 superseded_by → 放入 "Superseded Decisions" 区，标题加删除线
      4. 全量重写 docs/DECISIONS.md，格式：

      # Architecture Decisions
      > Auto-generated from GSD decision register. Do not edit directly.
      > Last synced: {当前日期} ({milestoneId}/{sliceId})
      ## Active Decisions
      ### D00x: {decision title}
      - **When:** {when_context}
      - **Choice:** {choice}
      - **Rationale:** {rationale}
      - **Revisable:** {revisable}
      ## Superseded Decisions
      ### ~~D00x: {title}~~ → Superseded by D00y

      ## 任务 2: docs/CONVENTIONS.md

      1. 读取 .gsd/KNOWLEDGE.md 全文
      2. 只提取 Rules 和 Patterns，跳过 Lessons Learned
      3. 同一 scope 下描述相同 pattern 的多条只保留编号最大的
      4. 全量重写 docs/CONVENTIONS.md，格式：

      # Coding Conventions
      > Auto-generated from GSD knowledge base. Do not edit directly.
      > Last synced: {当前日期} ({milestoneId}/{sliceId})
      ## Rules
      - **K00x** [{scope}]: {rule description}
      ## Patterns
      - **P00x** [{scope}]: {pattern description}

      注意：docs/ 目录不存在则先创建。不要修改 .gsd/ 下的任何文件。
    artifact: "DOCS-SYNCED.md"
    max_cycles: 1
    enabled: true
  - name: sync-milestone-docs
    after:
      - complete-milestone
    prompt: |
      你的任务是从 .gsd/ 全量重建三份项目级文档。

      ## 任务 1: docs/ARCHITECTURE.md
      读取 .gsd/milestones/{milestoneId}/{milestoneId}-RESEARCH.md、
      .gsd/milestones/{milestoneId}/{milestoneId}-SUMMARY.md、
      docs/DECISIONS.md，综合重写 docs/ARCHITECTURE.md：
      # Architecture
      > Auto-generated. Do not edit directly.
      > Last updated: {当前日期} after {milestoneId}
      ## System Overview / ## Component Map / ## Data Flow / ## Key Constraints / ## Tech Stack
      如果已有 ARCHITECTURE.md，保留仍准确的内容，更新不一致的部分。

      ## 任务 2: docs/CHANGELOG.md
      遍历 .gsd/milestones/ 所有目录，读取 SUMMARY.md，全量重写 docs/CHANGELOG.md。
      最新 milestone 排最前。格式：
      # Changelog
      ## {milestoneId}: {title} ({日期})
      ### {sliceId}: {title}
      - {要点}
      - Key files: {路径}

      ## 任务 3: CLAUDE.md
      检查根目录 CLAUDE.md 是否有 "# Project Knowledge (auto-maintained by GSD)" 段落。
      没有则追加，有则更新时间戳。追加内容：
      # Project Knowledge (auto-maintained by GSD)
      > Last synced: {当前日期} after {milestoneId}
      Architecture, decisions, conventions, and changelog are in docs/.
      Read them before making changes.

      不要修改 .gsd/ 下的任何文件。
    artifact: "MILESTONE-DOCS-SYNCED.md"
    max_cycles: 1
    enabled: true
pre_dispatch_hooks:
  - name: convention-patrol
    before:
      - execute-task
    action: modify
    append: |

      ## Convention Patrol
      在编码过程中，如果你发现当前任务涉及的文件中现有代码与 docs/CONVENTIONS.md 的规范不一致，
      请调用 gsd_save_knowledge 记录：type=lesson_learned, entry="发现 {文件} 的 {行为} 与 {规范编号} 不符", scope=global。
      只关注当前任务涉及的文件，按规范写新代码，不改不在任务范围内的旧代码。
      如果 docs/CONVENTIONS.md 不存在则跳过。
    enabled: true
---

# GSD Skill Preferences

See `~/.gsd/agent/extensions/gsd/docs/preferences-reference.md` for full field documentation and examples.
