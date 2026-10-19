namespace MASS.Design;

/** 消息输入、输出、思考与工具结果共用；按 ACP 的五种标准 type 区分。 */
union ContentBlock {
  text: TextContent,
  image: ImageContent,
  audio: AudioContent,
  resource: EmbeddedResource,
  resourceLink: ResourceLink,
}

model TextContent {
  type: "text";
  text: string;
  annotations?: ContentAnnotations;
}

model ImageContent {
  type: "image";

  /** 完整图片数据的 Base64 字符串，不含 data URI 前缀。 */
  data: string;

  mimeType: string;

  /** 可选来源 URI，不替代必填 data。 */
  uri?: string;

  annotations?: ContentAnnotations;
}

model AudioContent {
  type: "audio";

  /** 完整音频数据的 Base64 字符串，不含 data URI 前缀。 */
  data: string;

  mimeType: string;
  annotations?: ContentAnnotations;
}

model EmbeddedResource {
  type: "resource";

  /** 文本与二进制内容互斥；资源 URI 标识内容，不要求额外读取。 */
  resource:
    | {
        uri: string;
        mimeType?: string;
        text: string;
        blob?: never;
      }
    | {
        uri: string;
        mimeType?: string;

        /** Base64 字符串。 */
        blob: string;

        text?: never;
      };

  annotations?: ContentAnnotations;
}

model ResourceLink {
  type: "resource_link";
  uri: string;
  name: string;
  mimeType?: string;
  title?: string;
  description?: string;

  @minValue(0)
  size?: integer;

  annotations?: ContentAnnotations;
}

/** 内容的展示和使用提示，不承载事件完成状态。 */
model ContentAnnotations {
  audience?: ("user" | "assistant")[];

  @minValue(0)
  @maxValue(1)
  priority?: numeric;

  lastModified?: utcDateTime;
}
