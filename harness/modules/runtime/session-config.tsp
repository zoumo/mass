namespace MASS.Design;

/** 采用 ACP 的选择/布尔选项及展示元信息；不引入 ACP 传输协议。 */
union SessionConfigOption {
  select: SelectConfigOption,
  boolean: BooleanConfigOption,
}

model ConfigOptionInfo {
  id: string;
  name: string;
  description?: string;

  /** 开放的语义分类，例如 mode、model、model_config、thought_level。 */
  category?: string;
}

model SelectConfigOption extends ConfigOptionInfo {
  type: "select";
  currentValue: string;

  /** 平铺与分组互斥，不在同一数组混用；保留原生顺序。 */
  options: ConfigOptionValue[] | ConfigOptionGroup[];
}

model BooleanConfigOption extends ConfigOptionInfo {
  type: "boolean";
  currentValue: boolean;
  options?: never;
}

model ConfigOptionValue {
  value: string;
  name: string;
  description?: string;
}

model ConfigOptionGroup {
  group: string;
  name: string;
  options: ConfigOptionValue[];
}

/** 主 Session 初始期望值；空字符串省略，非空原样传入 SDK，不翻译别名。 */
model InitialSessionConfig {
  `model`?: string;
  effort?: string;
}

/** 单项瞬态更新；value 非空、有效 UTF-8 且无 NUL。 */
model ConfigChange {
  id: "model" | "effort";

  @minLength(1)
  value: string;
}

/** 请求与接受阶段证据，不声明当前 turn 已切换或值已生效。 */
model ConfigReceipt {
  change: ConfigChange;
  acceptance: AcceptanceEvidence;
}
