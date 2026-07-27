# backend/AGENTS.md

## Scope

本文件适用于 `backend/`。根目录 `AGENTS.md` 的跨端契约仍然有效。

## Backend Overview

后端是单 Go 运行时：提供 `/api/v1/*`、Swagger、健康检查，并在镜像中托管 `frontend/out` 静态资源。核心业务包括认证、用户、对话、模型渠道与能力、文件/RAG、MCP 工具、原生工具、计费、设置、审计、系统事件和可观测性。

## Layout

- `cmd/server/main.go`: 二进制入口和 Swagger 注释入口。
- `internal/cli/server.go`: server 启动逻辑。
- `internal/app/`: 依赖装配、运行时初始化、静态资源挂载。
- `internal/domain/<area>/`: 领域类型、常量和业务语义。
- `internal/application/<area>/`: service、用例编排、跨仓储协调。
- `internal/repository/*.go`: application 依赖的仓储接口。
- `internal/infra/persistence/models/`: Gorm model 和表名。
- `internal/infra/persistence/schema/`: AutoMigrate、扩展和 schema 验证。
- `internal/infra/persistence/postgres/`: 仓储实现；SQLite 路径复用兼容实现时按现有模式写测试。
- `internal/infra/{llm,mcp,extract,embedding,objectstore,cache,geoip,observability}`: 外部系统适配。
- `internal/transport/http/<area>/`: `router.go`、`module.go`、`handler*.go`、`dto*.go`。
- `internal/shared/response`: 统一响应 envelope 和错误码。

## Commands

```bash
go mod download
make run
make build
make test
go vet ./...
make fmt
make swagger
make tidy
```

`make swagger` 需要 `swag`；缺失时按 Makefile 提示安装。

## Layering Rules

- 请求链路保持 `transport/http -> application -> repository interface -> infra implementation`。
- Handler 负责绑定/校验 HTTP 入参、取认证上下文、调用 service、转换响应；不要把业务流程写进 handler。
- Application 不直接依赖 Gin、Gorm、Redis、Docker 或具体 provider client；外部能力通过 repository 或 adapter 接口进入。
- Repository 接口放 `internal/repository`；Gorm、SQL、事务和数据库错误翻译放 `internal/infra/persistence/*`。
- Domain 包只表达业务概念，不依赖 transport DTO、Gorm model 或 HTTP 细节。
- `internal/shared` 只放当前服务内横切能力，不作为随手新增 helper 的落点。

## HTTP And DTO Rules

- 标准响应只用 `response.Success`、`response.SuccessPage`、`response.Error*`、`response.InvalidRequestBody`。
- HTTP DTO 放对应 `internal/transport/http/<area>/dto*.go`；application DTO 放 `internal/application/<area>/dto.go`。
- 不让 Gorm model 直接成为 HTTP 响应结构。
- 分页返回 `response.SuccessPage` 或等价 `data.total/results` 结构。
- 新路由在对应 `router.go` 注册，并同步 Swagger 注释和 `backend/docs` 生成物。

## Error, Context, Transaction

- 业务可判定错误按现有 `errs.go` 风格定义，handler 按语义映射为公开错误。
- 外部调用、仓储操作和长流程必须接收并传递 `context.Context`；HTTP 入口使用 `c.Request.Context()`。
- 多表写入、余额流水、审计/系统事件联动、文件状态更新等一致性流程放进 repository transaction，沿用 `db.WithContext(ctx).Transaction(...)`。
- 数据库错误在 persistence 层翻译，不把 Gorm 原始错误泄漏到 transport。

## Configuration And Runtime Settings

- 静态基础设施配置来自根目录 `config.yaml`，环境变量优先；从 `backend/` 启动时读取 `../config.yaml`。
- 运行时业务设置来自数据库 settings；不要把 OTLP header、JWT secret、数据加密密钥、对象存储密钥等部署层密钥改成后台运行时配置。
- `APP_ENV=prod` 下的 JWT、加密密钥、CORS、公开 URL 安全检查不能放宽，除非任务明确要求并解释风险。

## Data And Persistence

- Schema 变更改 `internal/infra/persistence/models` 和 `schema`，并补/更新 schema 相关测试。
- 财务流水、审计日志、系统事件、文件对象、向量数据保持独立事实源，不合并成“方便查询”的混合表。
- SQL/Gorm 查询必须带用户或管理员权限边界；用户路径不要通过前端传参扩大数据范围。

## Observability

- 日志使用现有 zap/logger 链路，`/healthz` 不应产生访问噪音。
- Trace 覆盖请求、数据库、Redis、S3、出站 HTTP、LLM/MCP/文件提取关键链路；不要写入 prompt、文件内容、工具参数或密钥。
- 审计事件沿用各 handler 的 `recordAudit` / service 审计模式。

## Backend Testing

- 单元测试就近放 `_test.go`，优先覆盖 application、domain、shared、response、security、model routing、billing、file policy。
- 仓储测试优先用现有 SQLite/Gorm 测试模式，避免依赖真实外部 PostgreSQL、Redis、S3 或 LLM。
- Handler 测试沿用已有 Gin/router 测试结构，校验 envelope、状态码、权限和错误码。
- 改认证、权限、计费、余额、文件删除/配额、模型工具治理、参数锁定时必须补错误路径测试。
