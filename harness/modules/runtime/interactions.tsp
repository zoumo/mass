import "../../shared/models.tsp";

namespace MASS.Design;

/** Session 级交互；sessionId 来自事件信封，原运行及工作关联保留在内部。 */
union InteractionRequest {
  permission: PermissionInteractionRequest,
  form: FormInteractionRequest,
  url: UrlInteractionRequest,
}

/** 参考 ACP 权限请求；拒绝应选择原生提供的 reject 选项，不冒充取消。 */
model PermissionInteractionRequest {
  kind: "permission";
  toolCallId: string;
  title?: string;
  content?: ContentBlock[];

  @minItems(1)
  options: PermissionOption[];
}

model PermissionOption {
  optionId: string;
  name: string;

  /** ACP 标准提示值；选择权限始终依据 optionId，不靠 kind 猜测。 */
  kind: "allow_once" | "allow_always" | "reject_once" | "reject_always";
}

/** 参考 ACP form elicitation；schema 保持开放，由具体交互校验。 */
model FormInteractionRequest {
  kind: "form";
  message: string;
  requestedSchema: Record<unknown>;
}

/** 参考 ACP URL elicitation；仅通知外部流程，不自动打开 URL。 */
model UrlInteractionRequest {
  kind: "url";
  message: string;
  url: string;
}

model InteractionRequestedEventPayload {
  interaction: InteractionRef;
  request: InteractionRequest;

  /** 是否阻塞当前工作；非阻塞交互不强制改变 SessionState。 */
  blocking: boolean = true;
}

model InteractionClosedEventPayload {
  interaction: InteractionRef;

  /** answered 表示回答已接受；completed 表示外部流程明确完成。
   * 两者均不证明工具执行或整个工作成功。 */
  outcome: "answered" | "completed" | "cancelled" | "expired" | "failed";

  reason?: string;
}
