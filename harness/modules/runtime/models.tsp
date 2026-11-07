import "../../shared/models.tsp";

namespace MASS.Design.Execution;

/** Session 级 MCP server 声明；Runtime 可保存默认列表，Session 可按名称覆盖。 */
model MCPServer {
  /** 在同一个有效列表中唯一；具体 Runtime 类型可追加名称约束。 */
  @minLength(1)
  name: string;

  /** stdio 使用 command/args/env/cwd，http 与 sse 使用 url/headers。 */
  type: "stdio" | "http" | "sse";

  /** stdio 必填，表示启动 MCP server 的命令。 */
  command?: string;

  /** stdio 参数。 */
  args?: string[];

  /** stdio 子进程环境变量。 */
  env?: Record<string>;

  /** stdio 工作目录；具体 Runtime 类型决定是否支持。 */
  cwd?: string;

  /** http 或 sse 必填。 */
  url?: string;

  /** http 或 sse 请求头。 */
  headers?: Record<string>;

  /** 由环境变量名解析 HTTP 请求头；具体 Runtime 类型决定是否支持。 */
  envHeaders?: Record<string>;

  /** 从指定环境变量取得 Bearer token；具体 Runtime 类型决定是否支持。 */
  bearerTokenEnvVar?: string;
}

/** 运行环境中的会话选项，不含资源名称、实例引用或版本字段。 */
model DriverSessionSpec {
  cwd: string;
  systemPrompt?: string;
  additionalDirs?: string[];

  /** Service 已完成 Runtime 默认值与 Session 显式值的字段级合并；仍省略的字段使用各自 schema 默认值。 */
  features?: SessionFeatures;

  /** 本次 Session 的有效 MCP server 列表；Service 已按名称合并 Runtime 默认值与 Session 显式项。 */
  mcpServers?: MCPServer[];
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
