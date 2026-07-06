# AGENTS.md

## Project Overview

本仓库是 [DEEIX-AI/DEEIX-Chat](https://github.com/DEEIX-AI/DEEIX-Chat) 的分支项目，后续按独立项目开发和运行；当前 Go module、Docker 镜像名和部分文档仍保留上游命名，除非任务明确要求迁移。

DEEIX Chat 是可部署的 AI 平台：后端提供 Gin HTTP API、认证授权、模型路由、对话、文件/RAG、MCP 与原生工具、计费、设置、审计、静态前端托管；前端是 Next.js App Router 静态导出的聊天与管理界面。业务规则、权限、计费、模型参数治理、文件处理策略以后端为准，前端只做交互、展示、轻量校验和 API 调用。

## Repository Layout

- `backend/`: Go 1.26 后端服务。
- `backend/cmd/server/`: 服务入口。
- `backend/internal/cli/`: 启动命令与 server wiring。
- `backend/internal/app/`: 应用装配和基础设施初始化。
- `backend/internal/domain/`: 领域类型和核心业务概念。
- `backend/internal/application/`: 用例编排和跨仓储业务流程。
- `backend/internal/repository/`: 仓储接口。
- `backend/internal/infra/`: 配置、持久化、缓存、对象存储、LLM/MCP、观测等实现。
- `backend/internal/transport/http/`: Gin 路由、handler、HTTP DTO、中间件。
- `backend/internal/shared/`: 响应 envelope、request metadata、安全等横切能力。
- `backend/docs/`: Swagger 生成物。
- `frontend/`: Next.js 16 / React 19 前端。
- `frontend/app/`: App Router 路由和 layout；保持薄层。
- `frontend/features/`: chat、admin、files、settings、recent 等业务域。
- `frontend/shared/`: 跨业务 API、auth、hooks、components、lib、generated。
- `frontend/components/ui/`: 基础 UI 组件。
- `docker/`: Tika、Docling、OCR 等可选文件处理服务。
- `scripts/`: 跨端版本同步脚本。
- `config*.example.yaml`: 根目录运行配置模板。
- `docker-compose*.yml`: 默认、全量、SQLite 部署方案。

## Setup Commands

从仓库根目录执行：

```bash
(cd backend && go mod download)
(cd frontend && corepack enable && pnpm install)
```

按运行模式复制配置：

```bash
cp config.example.yaml config.yaml        # 外部 PostgreSQL + Redis
cp config.full.example.yaml config.yaml   # compose 内置 PostgreSQL + Redis
cp config.sqlite.example.yaml config.yaml # SQLite + memory cache
```

前端本地开发配置：

```bash
cd frontend
cp .env.example .env.local
```

`config.yaml`、`.env.local`、`storage/`、`data/` 是本地运行状态，不要提交。

## Development Commands

后端：

```bash
cd backend
make run
make build
make swagger
```

前端：

```bash
cd frontend
pnpm dev
pnpm lint
pnpm build
```

全容器运行：

```bash
docker compose -f docker-compose.full.yml up -d
```

本地改源码时只启动依赖，再分别在两个终端跑后端和前端：

```bash
docker compose -f docker-compose.full.yml up -d postgres redis
(cd backend && make run)
(cd frontend && pnpm dev)
```

轻量容器试用：

```bash
cp config.sqlite.example.yaml config.yaml
docker compose -f docker-compose.sqlite.yml up -d
```

可选文件处理服务：

```bash
docker compose -f docker/tika/docker-compose.yml up -d
docker compose -f docker/docling/docker-compose.yml up -d --build
docker compose -f docker/tesseract/docker-compose.yml up -d --build
docker build -t deeix-chat-rapidocr docker/rapidocr
```

版本同步：

```bash
node scripts/sync-version.mjs backend
node scripts/sync-version.mjs frontend
```

数据库 schema 由后端 `internal/infra/persistence/models` 和 `internal/infra/persistence/schema` 管理，目前没有单独迁移命令。

## Testing Instructions

后端：

```bash
cd backend
make test
go vet ./...
make build
```

改 HTTP 路由、DTO 或 Swagger 注释后：

```bash
cd backend && make swagger
```

前端：

```bash
cd frontend
pnpm lint
pnpm build
```

当前 `frontend/package.json` 没有 `test`、`typecheck`、组件测试或 E2E 脚本；不要编造命令。涉及路由、Next 配置、依赖、导出构建或类型形状时，用 `pnpm build` 做验证。

跨端 API 变更至少跑：

```bash
(cd backend && make test && make swagger)
(cd frontend && pnpm lint && pnpm build)
```

当前没有独立集成测试、前端单测、组件测试或 E2E 命令；如任务新增这些能力，同步更新本文件和对应 package/Makefile 脚本。

## Go Backend Conventions

详细规则见 `backend/AGENTS.md`。根层只强调边界：启动链路是 `cmd/server -> internal/cli -> internal/app`；请求链路是 `transport/http -> application -> repository -> infra`；HTTP 响应用 `internal/shared/response`；schema 改动同步 models/schema/test；接口改动同步 Swagger 和前端手写类型。

## React Frontend Conventions

详细规则见 `frontend/AGENTS.md`。根层只强调边界：`app/` 保持薄路由，业务进 `features/*`，跨域复用才进 `shared/*`；API 调用走 `shared/api` 或 feature API；样式和组件沿用 Tailwind、`components/ui`、Radix/Base UI、lucide-react；前端不决定权限、计费、模型路由或文件处理策略。

## API Contract Rules

- 后端 Swagger 源在 Go 注释和 `backend/docs/{docs.go,swagger.json,swagger.yaml}`；生成命令是 `cd backend && make swagger`。
- 前端没有生成 client；类型手写在 `frontend/shared/api/*.types.ts` 和 `frontend/features/*/api/*.types.ts`。
- 标准响应 envelope 是 `errorMsg`, `errorCode?`, `details?`, `requestId?`, `data`；分页数据放在 `data.total` 和 `data.results`。
- 后端响应统一用 `backend/internal/shared/response`，不要新增平行 response 包或 raw `errorMsg` JSON。
- 改接口必须同步 handler DTO、Swagger 注释/生成物、前端 API 类型、调用点和相关测试/fixture。
- 不要悄悄改 JSON 字段名。破坏性改动需要在后端和前端同一变更里完成。

## Security And Data Handling

- 不提交 `config.yaml`、`.env*local`、真实数据库、上传文件、对象存储导出、生产密钥或真实用户数据。
- 日志、trace、错误 details 中不要输出 JWT、refresh token、API key、OAuth/OIDC secret、Turnstile/Stripe secret、prompt、文件内容、工具参数或 PII。
- 前端 access token 只按现有会话模型保存在内存；refresh token 由后端 HttpOnly Cookie 管理。
- 用户数据访问必须带认证用户上下文；管理员路径要沿用现有权限检查和审计记录。
- 模型能力 JSON、计费、工具启停、文件处理策略和参数锁定以后端治理为准。

## PR / Commit Rules

- Commit subject 必须匹配 CI：`type: subject`。
- 允许 type：`build`、`chore`、`ci`、`docs`、`feat`、`fix`、`perf`、`refactor`、`revert`、`style`、`test`。
- subject 只能使用英文大小写字母、数字、空格、句点、连字符和下划线。
- PR 说明写问题、做法、验证命令；行为、配置、部署或 API 契约变化要更新文档。
- 行为改变补测试；安全、权限、计费、文件处理、模型路由、会话 token 相关改动必须优先补测试。
- 不把生成物放进提交，除非项目要求该生成物作为契约提交；Swagger 生成物属于需要同步的契约文件。

## Nested AGENTS.md Strategy

根目录文件写跨端规则、常用命令和 API 契约。`backend/AGENTS.md` 写 Go 后端专属规则，`frontend/AGENTS.md` 写 React/Next 专属规则。编辑文件时，离目标文件最近的 `AGENTS.md` 优先；用户显式指令优先于所有 `AGENTS.md`。
