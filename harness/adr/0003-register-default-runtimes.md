---
type: Architecture Decision Record
title: 启动时登记默认 Claude 和 Codex Runtime
description: 记录默认 Runtime 的登记时机、配置所有权和 Session 权限选择。
status: stable
sources:
  - id: direction
    resource: 项目负责人在 2026-11-22 确认启动时补齐 Claude 和 Codex、默认 fullAccess，并允许外部修改实例
    title: 默认 Runtime 的登记和管理
---

# 启动时登记默认 Claude 和 Codex Runtime

`massd` 在开放业务接口前按名称补齐 `claude` 和 `codex`，用户可以直接用这两个名称创建 Session。已有同名实例优先。登记产生普通持久 Runtime，客户端可以通过现有接口更新或删除实例。删除后，下一次启动会补齐该名称。Agent CLI 从 `massd` 的 `PATH` 按需解析，因此 CLI 缺失时仍能启动 `massd`。[^direction]

新登记实例使用 `fullAccess` 作为新建 Session 的权限默认值。调用者可以在 Runtime 上修改默认值，也可以在创建 Session 时明确指定权限。创建时将选定的权限写入 Session spec，恢复沿用该值。登记时机和权限解析分别见 [Service](../modules/service/design.md#服务重启与状态校正) 和 [Session 配置](../modules/runtime/session-config.md#初始配置与恢复)。

[^direction]: 负责人要求默认实例和手动登记的实例采用相同的管理规则。
