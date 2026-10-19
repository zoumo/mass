import "./types.tsp";
import "./states.tsp";
import "./content-blocks.tsp";

namespace MASS.Design;

/** HistoryStore 的不透明分页位置；调用方不得解析、排序或自行构造。 */
@minLength(1)
scalar HistoryCursor extends string;

model SessionInput {
  content: ContentBlock[];
}

model InputReceipt {
  submission: SubmissionRef;
  acceptance: AcceptanceEvidence;
}

model InteractionReceipt {
  interaction: InteractionRef;
  evidence: InteractionEvidence;
}

model CancelReceipt {
  /** noActiveWork 时不提供 target；controlAcknowledged 时必须提供接纳时固定的工作引用。 */
  target?: WorkRef;

  result: "controlAcknowledged" | "noActiveWork";
}

/** 所有接口共用的错误结构；不包装成功业务值。 */
@error
model OperationError {
  /** 稳定错误分类；outcome_unknown 表示副作用结果无法确认，不是确定失败。 */
  code: string;

  /** 可供程序判断的具体原因标识，不包含动态描述或原生错误文本。 */
  reason: string;

  /** 面向人的脱敏说明；调用者不通过解析此字段判断错误类型或是否重试。 */
  message: string;
}
