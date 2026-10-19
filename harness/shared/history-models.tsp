import "../modules/runtime/session-events.tsp";

namespace MASS.Design;

model HistorySyncProgress {
  /** 按持久追加顺序最后同步且可读取的 eventId；尚无事件时为 null。
   * ID 不表示大小或顺序，也不代表已追平上游。 */
  syncedEventId: string | null;
}

/** Service 与 Store 共用，不在转发中丢失过滤或同步进度。 */
model HistoryQuery {
  /** 按 SessionEvent.type 精确匹配，多个类型之间为 OR；省略表示全部类型。
   * 实时类型无持久记录可返回。 */
  @minItems(1)
  types?: SessionEventType[];

  /** 从上次返回的分页游标之后继续；省略则从第一条开始。
   * 游标绑定 Session 与过滤条件，不是 eventId 或时间。 */
  after?: HistoryCursor;

  /** 默认 100；实现若有容量上限，超限明确报错。 */
  @minValue(1)
  limit?: integer = 100;
}

model HistoryPage {
  events: SessionEvent[];

  /** 存在后续匹配事件时提供；后续分页保持 Session 与过滤条件不变。 */
  next?: HistoryCursor;

  /** 本次读取视图中最后同步的 eventId，不受类型过滤影响。 */
  syncedEventId: string | null;
}
