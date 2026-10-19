import "../../shared/models.tsp";

namespace MASS.Design.Execution;

/** 运行环境中的会话选项，不含资源名称、实例引用或版本字段。 */
model DriverSessionSpec {
  cwd: string;
  systemPrompt?: string;
  additionalDirs?: string[];
}

model DriverSession {
  sessionId: string;
  capabilities: CapabilitySet;
}

/** Service 在 open/resume 接纳时传入；adapter 只保存当前运行绑定，不查询资源目录。 */
model DriverSessionAddress {
  @minLength(1)
  `namespace`: string;

  @minLength(1)
  sessionName: string;
}
