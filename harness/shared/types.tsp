namespace MASS.Design;

/** 不透明引用；只在原 Session 与原运行上下文内使用，不解析其内容。 */
alias SubmissionRef = string;
alias WorkRef = string;
alias InteractionRef = string;

/** 开放的类型名称；具体 Registry 决定已实现哪些类型。 */
alias RuntimeType = string;

/** 类型专属配置；动态字段由 Registry 校验，秘密只保存引用。
 * unknown 明确允许动态值，不是待决占位。 */
alias RuntimeConfig = Record<unknown>;

/** 开放的能力名 → 是否支持；缺失表示未宣告支持，不能推断为支持。
 * 具体 Driver 定义能力名，只宣告本次运行实际可用的能力。 */
alias CapabilitySet = Record<boolean>;

/** 由对应交互约定的动态回答值；可为标量、数组、对象或 null。
 * unknown 是有意开放，接收方仍须按原交互校验。 */
alias InteractionAnswer = unknown;

/** 仅返回实际观察的接受证据；缺失表示未知，false 表示明确未达到该阶段。
 * sent 不得仅依据本地入队，须有向原生端交付的实际证据。
 * 原生接受或记录不证明执行完成、MASS 历史提交或资源释放。 */
model AcceptanceEvidence {
  sent?: boolean;
  nativeAccepted?: boolean;
  nativeRecorded?: boolean;
}

/** 与输入接受使用相同的证据语义，目标由 InteractionRef 固定。 */
alias InteractionEvidence = AcceptanceEvidence;
