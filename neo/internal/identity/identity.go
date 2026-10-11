// Package identity 收敛产品身份相关的唯一来源，
// 避免 vmconsole/neo 这类名字散落在源码、Dockerfile、docker-compose 多处。
//
// 注意：环境变量前缀必须同时与 Dockerfile、docker-compose.yml 中的 NEO_* 保持一致，
// 那两份文件无法引用本常量，属于刻意保留的「镜像构建期契约」。
package identity

// Name 是产品代号，对应仓库目录 /root/edge/neo，
// 也是后续把同目录下的服务融合为 all-in-one 应用时的统一标识。
const Name = "neo"

// EnvPrefix 是运行时环境变量前缀；Dockerfile 与 docker-compose 中的 NEO_* 必须与此一致。
const EnvPrefix = "NEO"
