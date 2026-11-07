namespace MASS.Design;

/** Service 对本实例拥有的运行资源的观察；不表示远端共享服务的存活状态。 */
enum RuntimeState {
  /** 未启动，或已确认本实例拥有的运行资源释放。 */
  stopped,

  starting,

  /** 已成功启动；不单独证明全部 Session 健康。 */
  running,

  /** 已进入停止协调，尚未确认全部运行资源释放。 */
  stopping,

  /** 无法确认当前运行状态；不得据此认为资源已经释放。 */
  `unknown`,
}

/** Session 的统一资源状态，不再拆分生命周期、连接和工作状态。 */
enum SessionState {
  /** 持久身份存在但未激活，MASS 当前无运行绑定且已结算其接纳的责任，可显式恢复；close 成功及正常 shutdown 释放后使用此值。
   * offline 不声称无法观测的 provider 工作或旧外部资源已经停止。 */
  offline,

  /** 正在创建或恢复运行连接。 */
  opening,

  /** 连接可用，已确认当前没有执行中的工作。 */
  idle,

  running,

  /** 当前工作需要用户采取行动后才能继续，例如回答问题或确认权限。 */
  requires_action,

  /** close 已接纳但尚未成功；无论本次尝试正在执行还是失败都保持此值，并通过 reason 表达原因，阻止新业务操作与冲突恢复。 */
  closing,

  /** 无法确认当前运行或工作状态，不能据此推断空闲或离线；服务重启时旧在线状态缺少释放/结算证明的校正为此值。
   * 唯一例外是 close 接纳且当前没有 SDK 句柄时，可在 MASS 内部清理后进入 offline，但不声称外部运行已经停止。 */
  `unknown`,
}
