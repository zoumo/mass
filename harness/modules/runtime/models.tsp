import "../../shared/models.tsp";

namespace MASS.Design.Execution;

/** 运行环境中的会话选项，不含资源名称、实例引用或版本字段。 */
model DriverSessionSpec {
  cwd: string;
  systemPrompt?: string;
  additionalDirs?: string[];

  /** 会话功能请求；字段省略时使用各自 schema 默认值。 */
  features?: SessionFeatures;
}

/** 可扩展的会话功能开关；新增字段必须自行声明默认值。 */
model SessionFeatures {
  /** 是否允许 Agent 向用户提问。省略时为 true，false 表示请求关闭。 */
  agentQuestions?: boolean = true;
}

model DriverSession {
  /** 原生会话 ID；不得为 . 或 ..，非法 ID 不得作为打开或恢复成功结果发布。 */
  sessionId: string;

  capabilities: CapabilitySet;

  /** 本次运行实际生效的功能值；请求开启但 adapter 不支持时降级为 false。 */
  effectiveFeatures: SessionFeatures;

  /** 仅显式输入创建返回，不持久、不代表工作完成。 */
  initialInputReceipt?: InputReceipt;
}

/** Service 在 open/resume/load 接纳时传入；adapter 只保存当前运行绑定，不查询资源目录。 */
model DriverSessionAddress {
  @minLength(1)
  `namespace`: string;

  @minLength(1)
  sessionName: string;
}
