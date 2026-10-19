import "../../shared/history-models.tsp";

namespace MASS.Design;

/** 按全局 sessionId 保存和读取历史；不启动 Runtime，不发布事件。
 * 文件保存完整记录，SQLite 仅为可重建索引。 */
interface HistoryStore {
  /** 按输入顺序追加同一 Session 的非空批次；保留 adapter 提供的 eventId/time。
   * 成功表示本批全部记录已持久保存且可读取，返回最后同步的 eventId。
   * 同一 sessionId/eventId 的相同事件允许重试，不重复追加、不改变原顺序。
   * 同一 eventId 内容冲突则报错；重试旧事件不能使同步进度倒退。
   * 这是同一 UUID 的写入幂等，不按原生 ID、itemId 或内容合并不同 UUID。
   * 出错不表示整批未写入；调用方须读取同步进度确认，不能假定整批事务。 */
  append(sessionId: string, @minItems(1) events: SessionEvent[]):
    | HistorySyncProgress
    | OperationError;

  /** 按持久追加顺序读取快照，以不透明游标分页；空页不表示已追平实时数据。
   * syncedEventId 与本次读取视图一致，不是排序键或分页位置。
   * 按 query.types 过滤 SessionEvent.type，不改变全会话同步进度。 */
  list(sessionId: string, query?: HistoryQuery): HistoryPage | OperationError;

  /** 返回按追加顺序最后同步、持久保存且可读取的 eventId；尚无事件时为 null。
   * 不是 SQLite 索引进度，不声明已追平上游，不包含未知的上游目标。 */
  getSyncProgress(sessionId: string): HistorySyncProgress | OperationError;
}
