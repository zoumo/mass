---
version: 1
mode: team
skill_discovery: auto
skill_staleness_days: 0
uat_dispatch: false
unique_milestone_ids: true
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
---

# GSD Skill Preferences

See `~/.gsd/agent/extensions/gsd/docs/preferences-reference.md` for full field documentation and examples.
