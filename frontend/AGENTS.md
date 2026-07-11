# frontend/AGENTS.md

## Scope

本文件适用于 `frontend/`。根目录 `AGENTS.md` 的跨端契约仍然有效。

## Frontend Overview

前端是 Next.js 16 App Router + React 19 应用，`next.config.ts` 使用 `output: "export"`，最终由 Go 后端托管静态资源。它负责聊天工作区、最近会话、文件页、用户设置、管理员后台、模型参数配置、MCP/原生工具选择和消息链路展示；业务决策以后端 API 返回为准。

## Layout

- `app/`: 路由、layout、页面挂载；不要堆复杂业务逻辑。
- `features/chat`: 对话、输入框、消息、附件、模型参数、MCP、处理/思考/工具链路。
- `features/admin`: 后台账户、模型、上游、计费、日志、登录、文件、工具、权限组。
- `features/files`: 文件管理、预览、删除、提取、配额展示。
- `features/settings`: 用户偏好、订阅、账户、安全设置。
- `features/recent`, `features/share`, `features/prompts`, `features/layouts`: 对应业务域。
- `shared/api`: 通用 API client 和跨域接口类型。
- `features/*/api`: 业务域私有 API 封装和 DTO。
- `shared/auth`: token、会话和鉴权辅助。
- `shared/components`, `shared/hooks`, `shared/lib`, `shared/model`: 真正跨业务复用的能力。
- `components/ui`: 基础 UI primitives。
- `public/`: 图片、PWA、pdfjs 静态资源。

## Commands

```bash
corepack enable
pnpm install
pnpm dev
pnpm lint
pnpm build
pnpm start
pnpm sync:icons
pnpm sync:pwa-assets
pnpm sync:version
```

本地 API 地址写入 `.env.local`：

```env
NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:8080
```

`NEXT_PUBLIC_DEV_AUTO_LOGIN`、`NEXT_PUBLIC_DEV_USERNAME`、`NEXT_PUBLIC_DEV_PASSWORD` 只用于本地开发。

## Testing

当前没有前端单测、组件测试、E2E 或单独 typecheck 脚本。验证规则：

- 普通 UI/API 改动：`pnpm lint`。
- 路由、Next 配置、依赖、静态导出、类型形状、环境变量读取：`pnpm build`。
- 变更 `package.json` 后必须提交 `pnpm-lock.yaml`。

## Component And Feature Organization

- `app/` 文件只挂载 feature 页面或 route layout。
- 业务代码优先放 `features/<domain>`；不要把某个业务域的临时逻辑提前上移到 `shared/`。
- 复杂 feature 按 `api/`、`components/`、`components/sections/`、`hooks/`、`model/`、`types/`、`utils/` 拆分，参考 `features/admin`。
- 页面入口组件负责组织板块；弹窗、表格、图表、编辑器、批量操作等清晰功能拆成同域子组件。
- 跨业务复用才放 `shared/components`、`shared/hooks`、`shared/lib`。

## API And State Rules

- 与后端交互统一走 `shared/api/http-client.ts`、`shared/api/authed-client.ts` 或 feature-level API 模块。
- 通用响应类型在 `shared/api/common.types.ts`；错误展示读取 `ApiError` / `errorMsg`。
- `shared/api/*.types.ts` 和 `features/*/api/*.types.ts` 是当前前端契约源，后端接口变更必须同步手写类型。
- 不在组件中散落 `fetch`；新增接口先建 API 函数和类型。
- access token 按现有 auth 模型保存在内存，refresh token 由后端 HttpOnly Cookie 处理。
- 不在前端硬编码 provider 私有模型规则、计费规则、文件处理策略或权限结论。

## UI And Styling

- 复用现有 `components/ui`、Radix/Base UI、Shadcn 风格组件、Tailwind class 组织方式。
- 图标优先用 `lucide-react`。
- 复杂异步 UI 至少处理 loading、empty、error、success 状态；错误文案来自 API error 或现有 i18n 文案。
- Markdown/代码/公式/mermaid 渲染走现有聊天消息组件和 Streamdown 管线，不另起渲染器。
- 模型能力 JSON 的 `defaultOptions`、`optionControls`、`nativeToolKeys` 只负责展示和提交；最终治理由后端执行。

## Routing Notes

主要路由：`/chat`、`/files`、`/recent`、`/setting/*`、`/admin/*`、分享页。新增路由时同步侧边栏/导航、权限守卫、空态和构建验证。

## Generated Assets

- `shared/generated/pwa-assets.ts` 由 `pnpm sync:pwa-assets` 生成。
- `shared/generated/lobehub-icon-manifest.ts` 由 `pnpm sync:icons` 生成。
- `predev` 和 `prebuild` 会检查版本并同步图标/PWA 资产；不要手动编辑生成文件，除非脚本输出就是本次变更。
