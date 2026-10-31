namespace MASS.Design;

/** 启动时选择唯一元数据权威后端；不是主从优先级，不允许故障时自动切换。 */
model MetaStoreConfig {
  backend: "sqlite" | "file" = "sqlite";
}
