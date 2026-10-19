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
  /** 持久身份存在，当前无运行连接且已确认无未结算工作，可恢复。 */
  offline,

  /** 正在创建或恢复运行连接。 */
  opening,

  /** 连接可用，已确认当前没有执行中的工作。 */
  idle,

  running,

  /** 当前工作需要用户采取行动后才能继续，例如回答问题或确认权限。 */
  requires_action,

  /** 正在归档或关闭；尚未确认资源释放，阻止冲突恢复。 */
  closing,

  /** 已明确归档，并确认会话运行资源释放；保留身份和历史。 */
  archived,

  /** 无法确认当前运行或工作状态，不能据此推断空闲或离线。 */
  `unknown`,
}
