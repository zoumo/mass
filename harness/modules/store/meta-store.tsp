import "../service/models.tsp";

namespace MASS.Design;

/** 面向一种资源类型的元数据存储；不执行业务校验或 Agent 操作；当前不提供 JSON Patch。
 * 文件保存完整数据，SQLite 仅作可重建索引；返回值均为资源快照。 */
interface MetaStore<Resource extends {}, Query extends {}, ListResult> {
  /** 名称冲突则失败；生成 uid、createdAt、resourceVersion，初始化 generation=1。
   * 输入包含完整初始 spec/status，系统维护的只读 metadata 不接受调用方赋值。 */
  @parameterVisibility(Lifecycle.Create)
  create(resource: Resource): Resource | OperationError;

  /** 在本资源类型内按 namespace/name 查询；不存在则报错。 */
  get(`namespace`: NamespaceName | "", name: ResourceName):
    | Resource
    | OperationError;

  /** 省略条件时查询全部资源；筛选与分页由具体资源查询合同决定。 */
  list(query?: Query): ListResult | OperationError;

  /** 完整替换资源；以 metadata.resourceVersion 原子检查并发冲突，不存在则报错。
   * namespace/name、uid、createdAt 不可修改；generation 由 Store 维护。
   * 持久内容变化生成新 resourceVersion，只有 spec 变化递增 generation。
   * 返回实际写入快照；不解析、合并或重放 JSON Patch。 */
  update(resource: Resource): Resource | OperationError;

  /** 原子比较预期 resourceVersion 并删除；版本不符则冲突且不修改。
   * 运行资源释放及引用检查由 Service 协调，不在本方法中调用 Driver。
   * 资源不存在时的行为仍待定义。 */
  delete(
    `namespace`: NamespaceName | "",
    name: ResourceName,
    resourceVersion: string,
  ): void | OperationError;
}

/** Runtime namespace 必须为空，列表不分页。 */
alias RuntimeMetaStore = MetaStore<Runtime, RuntimeQuery, Runtime[]>;

/** Session namespace 必须非空，列表允许跨 namespace 并保留分页。 */
alias SessionMetaStore = MetaStore<Session, SessionMetaQuery, Page<Session>>;

/** 除既有筛选外，允许 Service 按全局 sessionId 查询持久资源。 */
model SessionMetaQuery extends SessionQuery {
  sessionId?: string;
}
