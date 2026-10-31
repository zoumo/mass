import "../events/observation.tsp";
import "../../shared/models.tsp";

namespace MASS.Design;

/** Session 列表的不透明游标，绑定过滤条件；不与历史游标混用。 */
alias PageCursor = string;

model PageRequest {
  /** 排他游标，绑定除 limit 外的过滤条件；排序与并发规则见 Service 文档。 */
  after?: PageCursor;

  /** 默认 100；实现若有容量上限，超限明确报错，不静默截断请求值。 */
  @minValue(1)
  limit?: integer = 100;
}
