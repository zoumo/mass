---
version: 1
mode: solo
skill_discovery: auto
skill_staleness_days: 0
uat_dispatch: false
unique_milestone_ids: false
parallel:
  enabled: true
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
  - name: sync-project-docs
    after:
      - validate-milestone
    prompt: |
      从 .gsd/ 全量重建所有项目级文档。docs/ 不存在则创建。不修改 .gsd/ 下任何文件。

      ## 1. docs/DECISIONS.md
      读取 .gsd/DECISIONS.md（markdown table 格式），筛选 scope 为 global 或 architecture 的行。
      scope 为 task 或 slice 的跳过。
      按 superseded_by 字段分成 Active 和 Superseded 两张表。
      保持 table 格式，全量重写 docs/DECISIONS.md。格式：
      # Architecture Decisions
      > Auto-generated from GSD decision register. Do not edit directly.
      > Last synced: {日期} after {milestoneId}
      ## Active
      | # | When | Scope | Decision | Choice | Rationale | Revisable? |
      |---|------|-------|----------|--------|-----------|------------|
      | D00x | ... | ... | ... | ... | ... | ... |
      ## Superseded
      | # | When | Decision | Superseded By |
      |---|------|----------|---------------|
      | D00x | ... | ... | D00y |

      ## 2. docs/CONVENTIONS.md
      读取 .gsd/KNOWLEDGE.md，只提取 Rules 和 Patterns，跳过 Lessons Learned。
      同 scope 同 pattern 多条只保留编号最大的。保持 table 格式。全量重写 docs/CONVENTIONS.md。格式：
      # Coding Conventions
      > Auto-generated from GSD knowledge base. Do not edit directly.
      > Last synced: {日期} after {milestoneId}
      ## Rules
      | # | Scope | Rule |
      |---|-------|------|
      | K00x | {scope} | {description} |
      ## Patterns
      | # | Scope | Pattern | Where |
      |---|-------|---------|-------|
      | P00x | {scope} | {description} | {location} |

      ## 3. docs/ARCHITECTURE.md
      读取 .gsd/milestones/{milestoneId}/{milestoneId}-RESEARCH.md、
      {milestoneId}-SUMMARY.md、以及刚生成的 docs/DECISIONS.md。
      综合重写 docs/ARCHITECTURE.md（System Overview / Component Map /
      Data Flow / Key Constraints / Tech Stack）。
      已有则保留仍准确内容，更新不一致部分。
      头部：> Auto-generated. Do not edit directly.
      > Last updated: {日期} after {milestoneId}

      ## 4. docs/CHANGELOG.md
      遍历 .gsd/milestones/ 所有目录，读取 SUMMARY.md。
      全量重写，最新 milestone 排最前。格式：
      # Changelog
      ## {milestoneId}: {title} ({日期})
      ### {sliceId}: {title}
      - {要点}
      - Key files: {路径}

      ## 5. AGENTS.md（摘要层，~200 行上限）
      检查根目录 AGENTS.md 是否存在。
      如果不存在，创建完整文件（含 GSD:AUTO 标记区和空的用户自定义区）。
      如果已存在，只替换 <!-- GSD:AUTO:START --> 到 <!-- GSD:AUTO:END --> 之间的内容。
      标记外的用户自定义内容不动。

      自动区是摘要，不是副本。所有数据用 table 格式，内容规则：
      - Architecture: 一段话系统概述 + component 名称列表（不展开），末尾链接 docs/ARCHITECTURE.md
      - Key Decisions: 最近 10 条 active 决策，table 格式（# | Decision | Choice），不含 rationale，末尾链接 docs/DECISIONS.md
      - Rules: 全部 Rules 用 table 格式（# | Scope | Rule），Patterns 不列，末尾链接 docs/CONVENTIONS.md
      - 不包含 CHANGELOG 内容（给人看的，agent 不需要）

      自动区总行数控制在 100 行以内。如果 decisions 超过 10 条，只保留最近 10 条。

      不删除 AGENTS.md 标记外内容。
    artifact: "PROJECT-DOCS-SYNCED.md"
    max_cycles: 1
    enabled: true

pre_dispatch_hooks:
  - name: convention-patrol
    before:
      - execute-task
    action: modify
    append: |

      ## Convention Patrol
      如果发现当前任务涉及的文件中现有代码与 docs/CONVENTIONS.md 规范不一致，
      调用 gsd_save_knowledge(type=lesson_learned, entry="发现 {文件} 与 {规范编号} 不符", scope=global)。
      只关注当前任务文件，按规范写新代码，不改任务范围外旧代码。
      docs/CONVENTIONS.md 不存在则跳过。
    enabled: true
---

# GSD Skill Preferences

See `~/.gsd/agent/extensions/gsd/docs/preferences-reference.md` for full field documentation and examples.
