---
version: 1
mode: solo
skill_discovery: auto
skill_staleness_days: 0
uat_dispatch: false
unique_milestone_ids: false
parallel:
  enabled: true
  max_workers: 3
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
  skip_research: true
  skip_reassess: true
  skip_slice_research: true
  reassess_after_slice: false
post_unit_hooks:
  - name: sync-project-docs
    after:
      - validate-milestone
    prompt: |
      从 .gsd/ 更新 docs/ARCHITECTURE.md 和 docs/CHANGELOG.md。docs/ 不存在则创建。不修改 .gsd/ 下任何文件。
      不修改 AGENTS.md。

      ## 1. docs/ARCHITECTURE.md
      读取 .gsd/milestones/{milestoneId}/{milestoneId}-RESEARCH.md、
      {milestoneId}-SUMMARY.md、以及 .gsd/DECISIONS.md。
      综合重写 docs/ARCHITECTURE.md（System Overview / Component Map /
      Data Flow / Key Constraints / Tech Stack）。
      已有则保留仍准确内容，更新不一致部分。
      头部：> Auto-generated. Do not edit directly.
      > Last updated: {日期} after {milestoneId}

      ## 2. docs/CHANGELOG.md
      遍历 .gsd/milestones/ 所有目录，读取 SUMMARY.md。
      全量重写，最新 milestone 排最前。格式：
      # Changelog
      ## {milestoneId}: {title} ({日期})
      ### {sliceId}: {title}
      - {要点}
      - Key files: {路径}
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
      如果发现当前任务涉及的文件中现有代码与 .gsd/KNOWLEDGE.md 中的 Rules 不一致，
      调用 gsd_save_knowledge(type=lesson_learned, entry="发现 {文件} 与 {规范编号} 不符", scope=global)。
      只关注当前任务文件，按规范写新代码，不改任务范围外旧代码。
      .gsd/KNOWLEDGE.md 不存在则跳过。
    enabled: true
---

# GSD Skill Preferences

See `~/.gsd/agent/extensions/gsd/docs/preferences-reference.md` for full field documentation and examples.
