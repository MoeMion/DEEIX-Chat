# Open WebUI MCP 自定义 Header 机制研究报告

> 调研日期：2026-07-10
> Open WebUI 基线：v0.10.2，commit `ecd48e2f718220a6400ecf49eafd4867a38feb10`
> MCP 基线：稳定版协议 2025-11-25；Open WebUI 使用 Python MCP SDK 1.27.2
> 本项目基线：commit `7ca720fe6037ebec6b2e9761fdb7f2db188748d6`

## 1. 摘要

Open WebUI 的“向 MCP 服务器透传用户和会话信息”实际上由两套互补机制组成：

1. **连接级自定义 Header 模板**：管理员在每个 MCP/OpenAPI 连接上配置任意 Header JSON；Header 值中的 `{{USER_ID}}`、`{{CHAT_ID}}` 等占位符由 Open WebUI 后端在请求时展开。
2. **全局用户信息转发**：部署者启用 `ENABLE_FORWARD_USER_INFO_HEADERS` 后，Open WebUI 自动向工具服务器追加一组固定但可改名的用户、聊天和消息 Header；用户身份还可以改为短期 HS256 JWT。

这两套机制都不是 MCP 协议定义的身份模型，而是 Open WebUI Host 在 Streamable HTTP 传输层之上的扩展。它们的价值在于：MCP 服务在处理 `initialize`、`tools/list` 或 `tools/call` 前就能获得租户、用户和会话路由上下文；只在 `tools/call.params._meta` 中携带信息无法覆盖握手、工具发现、HTTP 网关鉴权和限流。

对 DEEIX Chat 的建议不是逐行复制 Open WebUI，而是采用以下目标设计：

- 保留本项目已有的 MCP Server 静态 Header JSON，将其升级为**服务端模板**，MVP 不需要数据库字段迁移。
- 从已认证上下文和已持久化实体派生模板变量；用户、会话、消息一律使用公开 ID，不向外暴露数据库自增 ID。
- 在一次 MCP 操作或一次对话运行开始时解析 Header，保证握手、工具发现、工具调用和关闭使用同一上下文。
- 协议保留头、认证头与业务自定义头分层合并；禁止管理员覆盖 `MCP-Session-Id`、`MCP-Protocol-Version`、`Content-Type` 等协议头。
- 先修正本项目现有 MCP 客户端的协议版本头、会话关闭和重定向边界，再开放动态身份 Header。
- 明文 Header 只适合路由和审计；需要下游信任身份时，应采用带 `aud`、短 `exp` 的每服务签名上下文，不能把用户登录 Token 直接透传给 MCP 服务。

## 2. 研究范围与证据基线

### 2.1 主要资料

- [Open WebUI MCP 文档：Custom Headers](https://docs.openwebui.com/features/extensibility/mcp/#custom-headers)
- [Open WebUI 环境变量：Forward Headers](https://docs.openwebui.com/reference/env-configuration/#enable_forward_user_info_headers)
- [Open WebUI 外部工具事件文档](https://docs.openwebui.com/features/extensibility/plugin/development/events/)
- [Open WebUI v0.10.2 源码](https://github.com/open-webui/open-webui/tree/ecd48e2f718220a6400ecf49eafd4867a38feb10)
- [MCP 2025-11-25 Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)
- [MCP 2025-11-25 生命周期](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)
- [MCP 2025-11-25 Tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)
- [MCP 2025-11-25 Authorization](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
- [Python MCP SDK 1.27.2 Streamable HTTP 客户端](https://github.com/modelcontextprotocol/python-sdk/blob/v1.27.2/src/mcp/client/streamable_http.py)
- [Python MCP SDK 1.27.2 ClientSession](https://github.com/modelcontextprotocol/python-sdk/blob/v1.27.2/src/mcp/client/session.py)

### 2.2 功能演进

| 版本 | 变化 | 设计启示 |
| --- | --- | --- |
| v0.8.0 | 增加 `ENABLE_FORWARD_USER_INFO_HEADERS`，向 MCP/OpenAPI 工具服务器转发用户、聊天和消息上下文 | 自动转发与连接自定义配置是不同能力 |
| v0.9.3 | 增加连接 Header 模板，最初在模型连接和 OpenAPI 工具服务器路径中展开 | 仅实现通用函数不等于所有出站路径都已接入 |
| v0.9.6 | 修复 MCP 连接曾“保存模板但运行时原样发送”的问题，并增加 `USER_EMAIL`、`USER_ROLE` | 必须对 MCP 完整握手链路做端到端捕获测试 |
| v0.10.x | 通用解析器增加用户消息、任务、文件和 User-Agent 等变量；全局用户身份支持短期签名 JWT | 文档承诺与通用内部能力应分开管理 |

相关变更证据：

- [动态 Header 模板 PR #24164](https://github.com/open-webui/open-webui/pull/24164)
- [MCP 模板未展开修复 PR #24822](https://github.com/open-webui/open-webui/pull/24822)
- [初始模板解析实现](https://github.com/open-webui/open-webui/commit/9907c0a25ae830d134af70022238715f834d20c6)
- [邮箱与角色变量变更](https://github.com/open-webui/open-webui/commit/ed73ef3d8df988b0e9646b82df5b1a453202ef8d)

这段演进史非常有参考价值：MCP 曾绕过已有的模板解析器，原因是 MCP 与 OpenAPI 使用不同执行入口。后续代码将运行时认证、模板 Header 和全局身份 Header 收敛进共享的 `build_tool_server_headers()`。本项目应从一开始就让同步、验证、发现和调用共享同一套 Header 构造核心。

## 3. 两套 Header 机制

### 3.1 连接级自定义 Header 模板

管理员在连接上填写 JSON 对象，例如：

~~~json
{
  "X-Tenant-User": "{{USER_ID}}",
  "X-Conversation": "{{CHAT_ID}}",
  "X-Response-Message": "{{MESSAGE_ID}}"
}
~~~

官方 MCP 文档承诺的变量如下：

| 变量 | 运行时值 | 非聊天场景 |
| --- | --- | --- |
| `{{USER_ID}}` | 已认证用户 ID | 用户存在时仍有值 |
| `{{USER_NAME}}` | 用户显示名 | 用户存在时仍有值 |
| `{{USER_EMAIL}}` | 用户邮箱 | 用户存在时仍有值 |
| `{{USER_ROLE}}` | 用户角色 | 用户存在时仍有值 |
| `{{CHAT_ID}}` | 当前聊天 ID | 空字符串 |
| `{{MESSAGE_ID}}` | 当前响应消息 ID | 空字符串 |

当前 v0.10.2 的通用代码还识别 `USER_MESSAGE_ID`、`USER_MESSAGE_PARENT_ID`、`FILE_ID`、`FILE_NAME`、`FILE_CONTENT_TYPE`、`TASK` 和 `USER_AGENT`。但是，这些变量是共享解析器的内部能力，MCP 文档只承诺上表六项，而且 MCP 路径不保证所有额外元数据都存在。实现兼容时应以文档契约为准，不能仅凭当前源码把内部变量当成稳定 API。

解析器位于 [`backend/open_webui/utils/headers.py`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/utils/headers.py#L62-L107)，语义是：

- 只替换 Header **值**，不替换 Header 名称。
- 占位符区分大小写，使用大写形式。
- 一个值中可以出现多个占位符，也可以嵌入普通字符串。
- 缺失上下文替换为空字符串。
- 未知占位符保持原文，不报错。
- 非字符串值先执行 Python `str(value)`。
- 使用逐 token 的 `str.replace`，没有模板表达式、条件、循环或代码执行面。

解析发生在服务端，不依赖浏览器提供最终身份值。用户对象来自认证依赖；聊天与消息 ID 来自后端构造的请求元数据。需要注意：聊天/消息 ID 仍然是相关性标识，不应被 MCP 服务当成独立授权凭据。

### 3.2 全局用户信息转发

启用 `ENABLE_FORWARD_USER_INFO_HEADERS=True` 后，Open WebUI 默认追加：

| Header | 值 |
| --- | --- |
| `X-OpenWebUI-User-Name` | 用户显示名 |
| `X-OpenWebUI-User-Id` | 用户 ID |
| `X-OpenWebUI-User-Email` | 用户邮箱 |
| `X-OpenWebUI-User-Role` | 用户角色 |
| `X-OpenWebUI-Chat-Id` | 当前聊天 ID |
| `X-OpenWebUI-Message-Id` | 当前消息 ID |

六个 Header 名称均可通过环境变量修改。用户四项会发往多类外部后端；聊天 ID 的范围较窄；消息 ID 只发给 OpenAPI/MCP 工具服务器，用于外部工具向 Open WebUI 的消息事件端点回推状态。

如果设置 `FORWARD_USER_INFO_HEADER_JWT_SECRET`，用户四项会被一个默认名为 `X-OpenWebUI-User-Jwt` 的 HS256 JWT 替代。当前源码中的 claims 为 `sub`、`email`、`name`、`role`、`iss=open-webui`、`iat`、`exp`，默认有效期 300 秒。聊天和消息 ID 仍为独立明文 Header。

两套机制的关键差异：

| 行为 | 连接模板 | 全局转发 |
| --- | --- | --- |
| 开启范围 | 单个连接显式配置 | 整个部署启用 |
| Header 名 | 任意 | 固定默认名，可通过环境变量改名 |
| 缺失聊天/消息值 | Header 存在，值为空 | 不追加该 Header |
| 用户身份签名 | 不支持 | 可切换为短期 HS256 JWT |
| 用途 | 兼容下游约定、租户路由 | 统一身份、审计、限流、事件回调 |

### 3.3 HS256 密钥如何配置和共享

这是 Open WebUI 实现中容易被忽略、但决定身份是否可信的部署前提：**密钥不通过 MCP 协议协商或分发，而是由运维系统在 Open WebUI 与 MCP Server 之间带外共享。**

在这条链路中，角色分工为：

| 角色 | 责任 | 是否持有密钥 |
| --- | --- | --- |
| Open WebUI 后端 | MCP Client，同时也是用户身份 JWT 的签发方 | 是 |
| 浏览器/最终用户 | 发起聊天，不参与签名 | 否 |
| MCP Server | JWT 验证方，在处理 MCP 请求前验证身份断言 | 是 |
| Secret Manager/部署系统 | 生成、投递、轮换同一份 secret | 是 |

Open WebUI 侧通过普通环境变量配置；[`FORWARD_USER_INFO_HEADER_JWT_SECRET` 官方说明](https://docs.openwebui.com/reference/env-configuration/#forward_user_info_header_jwt_secret)也明确要求外部目的地用同一 shared secret 验证 JWT：

~~~text
ENABLE_FORWARD_USER_INFO_HEADERS=true
FORWARD_USER_INFO_HEADER_JWT_SECRET=<shared-secret>
FORWARD_USER_INFO_HEADER_JWT=X-OpenWebUI-User-Jwt
FORWARD_USER_INFO_HEADER_JWT_EXPIRES_SECONDS=300
~~~

[`env.py`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/env.py#L874-L896) 读取配置后，[`utils/headers.py`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/utils/headers.py#L20-L59) 把 secret 的原始字符串交给 PyJWT，以 `HS256` 生成签名。Open WebUI 不提供 MCP Server 端变量名、验证中间件或密钥下发 API；MCP Server 必须自行定义配置，例如：

~~~text
OPENWEBUI_IDENTITY_JWT_SECRET=<the-same-shared-secret>
OPENWEBUI_IDENTITY_JWT_ISSUER=open-webui
OPENWEBUI_IDENTITY_JWT_HEADER=X-OpenWebUI-User-Jwt
~~~

两侧 secret 必须逐字节一致。它既不是 `WEBUI_SECRET_KEY` 的自动派生值，也不是 MCP OAuth access token、OAuth client secret 或 MCP `Authorization` Bearer token。运维人员可以选择复用某个值，但 Open WebUI 的该功能不会自动复用或读取这些其他密钥，生产环境也不应混用。

典型分发方式：

- 同一 Kubernetes 集群：由 External Secrets/CSI Driver 从 Vault、AWS Secrets Manager、GCP Secret Manager 等读取同一 secret，分别注入 Open WebUI 和 MCP Server Pod。
- 不同集群或账号：两个工作负载通过各自身份读取同一 Secret Manager 记录，或使用受审计的跨环境 secret replication。
- Docker Compose/单机：通过 Docker Secret 或权限受限的 env file 注入两个容器；不得把 secret 写进 compose 文件或 Git。

Open WebUI 当前只有一个全局 `FORWARD_USER_INFO_HEADER_JWT_SECRET`。因此多个 MCP Server 会得到用同一密钥签发的 JWT；任一 Server 泄漏该密钥后，都具备为其他共享该密钥的 Server 伪造用户身份的能力。它的 JWT 也没有 `aud` 和 `kid`，无法做目标 Server 绑定和按 key id 选择验证密钥。

Open WebUI 自身没有完整的无停机轮换协议。安全轮换需要 MCP Server 先进入“旧、新两把验证密钥均接受”的窗口，再切换 Open WebUI 签名密钥，等待至少 `JWT TTL + 最大时钟偏差` 后删除旧密钥。若 MCP Server 只能配置一把密钥，切换期间只能接受短暂失败或安排协调停机。

DEEIX Chat 不采用这个全局共享模型。第 9.6 节定义每个 MCP Server 独立 HS256 密钥、独立 `aud` 和 `kid` 的 MVP，以限制单个 Server 泄漏时的横向影响。

## 4. 控制面流程

### 4.1 配置入口

管理员在 External Tools 中新增类型为 MCP 的连接。前端 [`AddToolServerModal.svelte`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/src/lib/components/AddToolServerModal.svelte#L285-L374) 将 Header 文本解析为 JSON 对象，只拒绝非对象和数组，然后随连接一起提交。

后端 [`routers/configs.py`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/routers/configs.py#L208-L284)：

1. 仅允许管理员读取和更新工具服务器连接。
2. 把连接数组写入 `tool_server.connections` 配置。
3. 重新构建工具服务器缓存。
4. 对 OAuth 2.1 MCP 连接重新注册 OAuth 客户端。
5. 发布配置更新事件。

所追踪的持久化路径将 `tool_server.connections` 作为 JSON 写入 `config.value`；该层没有对 Header 或 Bearer key 做字段级加密。OAuth client 信息另有加密流程，但不能据此认为自由格式 Header 中的静态密钥也被加密。

### 4.2 连接验证

`POST /tool_servers/verify` 对 MCP 执行一次真实连接和 `tools/list`。验证路径会展开用户变量，但没有聊天/消息 metadata，所以 `CHAT_ID` 和 `MESSAGE_ID` 为空。验证完成后关闭 MCP client。

当前验证路径没有复用完整的 `build_tool_server_headers()`，因此全局自动转发与生产聊天路径存在差异。这个残余差异再次说明：我们的“测试连接”必须调用与运行时相同的 Header 构造器，只通过显式的 `ContextMode=probe` 控制哪些变量为空。

### 4.3 用户可见性

管理员配置 MCP Server 后，普通用户看到的是一个 `server:mcp:<server-id>` 工具项，而不是自行注册的 MCP 地址。后端在列出工具时同时检查：

- 连接类型为 MCP；
- 连接启用；
- 用户或用户组拥有访问授权；
- OAuth 连接是否已为该用户完成授权。

这保证了 Header 模板和密钥仍处于管理员控制面，普通用户只能选择已授权的能力。

## 5. 对话运行时流程

~~~mermaid
sequenceDiagram
    participant U as "用户/浏览器"
    participant API as "Open WebUI Chat API"
    participant MW as "工具解析中间件"
    participant HB as "Header Builder"
    participant MC as "MCPClient"
    participant SDK as "Python MCP SDK"
    participant MS as "MCP Server"
    participant LLM as "模型"

    U->>API: "发送消息并选择 server:mcp:<id>"
    API->>API: "认证用户，生成/读取 chat_id、message_id"
    API->>MW: "user + metadata + tool_ids"
    MW->>MW: "检查连接启用状态与用户/组访问权"
    MW->>HB: "认证模式 + 自定义模板 + 全局转发策略"
    HB-->>MW: "本轮固定 Header"
    MW->>MC: "connect(url, headers)"
    MC->>SDK: "创建带默认 Header 的 httpx client"
    SDK->>MS: "POST initialize"
    MS-->>SDK: "InitializeResult + 可选 MCP-Session-Id"
    SDK->>MS: "POST notifications/initialized"
    SDK->>MS: "POST tools/list"
    MS-->>SDK: "工具定义"
    MW->>LLM: "带 server-id 前缀的工具定义"
    LLM-->>MW: "tool call"
    MW->>MC: "call_tool(原始工具名, arguments)"
    SDK->>MS: "POST tools/call，沿用 Header 与 MCP session"
    MS-->>SDK: "CallToolResult"
    MW-->>LLM: "工具结果"
    API->>MC: "本轮 finally 中 disconnect"
    SDK->>MS: "可选 DELETE，终止 MCP session"
~~~

### 5.1 元数据建立

聊天入口 [`main.py`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/main.py#L1007-L1160) 从认证用户和请求体建立 metadata。新聊天由后端生成 `chat_id`；多模型调用为每个模型写入自己的 assistant `message_id`。随后 metadata 贯穿工具解析和响应处理。

在 Open WebUI 语义中：

- `CHAT_ID` 是应用层对话 ID。
- `MESSAGE_ID` 是本次响应/assistant message 的 ID。
- `USER_MESSAGE_ID` 是触发本轮的用户消息 ID，属于后续扩展变量。
- 登录 session ID 没有作为自定义模板变量公开。
- `MCP-Session-Id` 是 MCP Server 在初始化响应中签发的协议会话 ID，与聊天 ID 完全不同。

这几类 ID 不能混用。尤其不能把 `CHAT_ID` 写入 `MCP-Session-Id`；后者必须由 MCP 客户端和服务端协议栈管理。

### 5.2 Header 构造与优先级

运行时共享构造器见 [`utils/tools.py`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/utils/tools.py#L115-L174)，顺序为：

1. 根据认证模式产生基础认证 Header/Cookie。
2. 展开并合并连接自定义 Header。
3. 若启用全局转发，追加用户身份 Header/JWT。
4. 若 metadata 中有聊天/消息 ID，再追加对应全局 Session Header。

直接后果：

- 自定义 Header 可以覆盖步骤 1 生成的 `Authorization`。
- 全局用户 Header 会覆盖同名自定义 Header。
- 全局聊天/消息 Header 最后写入，会覆盖同名自定义 Header。
- 使用其他名字的模板 Header 会与全局 Header 同时存在。

Open WebUI 没有在这个构造器中禁止协议保留 Header。Python MCP SDK 会用每请求 Header 覆盖 httpx client 的同名默认 Header，因此 `Accept`、`Content-Type`、`MCP-Session-Id`、`MCP-Protocol-Version` 最终仍以 SDK 为准；但 `Authorization` 没有同样的协议覆盖保护。

### 5.3 MCP Client 生命周期

[`connect_mcp_server()`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/utils/middleware.py#L2112-L2160) 在真正出站前再次检查用户访问权，然后：

1. 构造一次 Header；
2. 创建一个 `MCPClient`；
3. 建立 Streamable HTTP session 并完成 initialize；
4. 调用 `tools/list`；
5. 按函数名过滤器裁剪工具；
6. 将服务 ID 前缀加到发给模型的工具名，避免多服务器重名；
7. 用闭包把模型工具名映射回 MCP 原始工具名。

同一个 client 在一轮聊天内处理该 MCP Server 的所有工具调用，Header 不会在每次 HTTP 请求前重新展开。它们因此是“每轮/每连接上下文”，不是“每个 tool call 独立上下文”。本轮结束时，client 在 `finally` 中清理；不同用户和不同消息不会共享该 client。

[`MCPClient`](https://github.com/open-webui/open-webui/blob/ecd48e2f718220a6400ecf49eafd4867a38feb10/backend/open_webui/utils/mcp/client.py#L59-L181) 把 Header 交给 SDK 的 httpx client。SDK 再对每个请求追加协议 Header，所以自定义 Header 会自然覆盖整个生命周期中的：

- `initialize`
- `notifications/initialized`
- `tools/list`
- `tools/call`
- 可选 GET/SSE 重连
- 可选 DELETE session termination

初始化有独立的 `MCP_INITIALIZE_TIMEOUT`，默认 10 秒；普通工具请求超时来自工具服务器 HTTP 配置。

## 6. MCP 协议细节

### 6.1 自定义身份 Header 不是 MCP 标准字段

MCP 规定 JSON-RPC 消息、生命周期、能力协商、Streamable HTTP 和可选 OAuth 授权，但没有定义 `X-OpenWebUI-User-Id` 或 `{{USER_ID}}`。这些 Header 的含义完全由 Open WebUI 与目标 MCP Server 约定。

因此，目标 MCP Server 必须显式读取这些 Header；标准 MCP Server 不会自动把它们注入 tool handler 上下文。反向代理若要消费它们，也必须建立可信来源、覆盖外部同名 Header，并限制直连绕过。

### 6.2 标准 Streamable HTTP Header

| Header | 所有者 | 规则 |
| --- | --- | --- |
| `Content-Type: application/json` | MCP transport | POST JSON-RPC 消息 |
| `Accept: application/json, text/event-stream` | MCP transport | POST 必须同时接受 JSON 和 SSE |
| `MCP-Protocol-Version` | MCP transport | initialize 后所有后续 HTTP 请求携带协商版本 |
| `MCP-Session-Id` | MCP Server/transport | 服务端可在初始化响应签发；客户端后续请求原样携带 |
| `Last-Event-ID` | SSE 恢复 | 恢复断开的 SSE stream |
| `Authorization: Bearer ...` | MCP OAuth/部署认证 | OAuth access token 必须放 Header，不得放 URL |
| `Origin` | HTTP 客户端/浏览器场景 | MCP Server 必须验证存在的 Origin，防 DNS rebinding |

业务模板不得管理前五项。认证 Header 也应从专门的认证配置产生，而不是与普通模板 Header 混合。

### 6.3 标准调用序列

一次正常 Streamable HTTP 连接至少包含：

1. `initialize` request：客户端提出协议版本、capabilities、clientInfo。
2. `notifications/initialized` notification：客户端确认进入 operation phase。
3. `tools/list` request：发现工具，可分页。
4. 一个或多个 `tools/call` request。
5. 客户端不再需要 session 时，可用携带 `MCP-Session-Id` 的 DELETE 终止。

每个客户端 JSON-RPC 消息都是一个新的 HTTP POST。服务器对 request 可返回单个 JSON，也可返回 SSE stream；客户端必须支持两者。

Open WebUI v0.10.2 通过 MCP SDK 1.27.2 请求最新稳定协议 `2025-11-25`，并由 SDK 保存服务端协商版本和 session ID。SDK 的每请求协议 Header 优先级高于 httpx 默认 Header。

### 6.4 Header 与 `_meta` 的分工

| 维度 | HTTP Header | JSON-RPC `params._meta` |
| --- | --- | --- |
| 代理、网关、WAF 可见 | 是 | 通常需要解析 body |
| initialize 前可用 | 是 | 只能随具体消息出现 |
| 覆盖 tools/list | 是 | 需在该请求中显式携带 |
| 适合认证、租户路由、限流 | 是 | 不适合作为 HTTP 网关认证 |
| 属于 MCP JSON-RPC 扩展面 | 否 | 是 |
| 易被日志/链路系统采集 | 高，需要主动脱敏 | 也需要脱敏 |

DEEIX Chat 当前已在 `tools/call.params._meta` 中发送 `user_id`、`conversation_id` 和 `request_id`。这对 MCP tool handler 有用，但无法满足握手前路由、`tools/list` 的用户化发现、HTTP 层鉴权或代理审计。后续可同时保留安全的 `_meta`，但不能用它替代 Header。

### 6.5 版本演进风险

本报告以 2025-11-25 稳定协议为准。[MCP draft changelog](https://modelcontextprotocol.io/specification/draft/changelog) 已提出移除协议级 session 和 `MCP-Session-Id` 的 stateless-first 方向，并增加新的请求 Header 约定。这仍是 draft，不应立即按草案改造生产实现，但应避免让业务 Header 模板硬编码或依赖 `MCP-Session-Id`，并把协议头管理集中在 transport 层，以便未来升级。

## 7. Open WebUI 实现评估

### 7.1 值得借鉴

- **服务端展开**：浏览器只提交模板，不提交最终用户身份。
- **访问检查在出站前**：未经授权的用户不会触发 MCP 连接。
- **模板功能很小**：纯字符串替换，没有模板代码执行风险。
- **一次运行一个隔离 client**：避免跨用户 Header 和 MCP session 污染。
- **同一 Header 覆盖完整生命周期**：初始化、发现和调用语义一致。
- **业务 ID 与 MCP session 分离**：聊天 ID 只是业务 Header。
- **认证模式与上下文 Header 同时存在**：身份提示不替代真正的 MCP OAuth。
- **可签名用户身份**：下游不必盲信四个明文用户 Header。

### 7.2 风险与限制

1. **模板曾遗漏 MCP 路径**
   v0.9.3 到 v0.9.6 的回归证明，多入口复制 Header 逻辑极易产生行为漂移。

2. **自定义 Header 可覆盖 Authorization**
   自定义连接 Header 后合并，管理员可以意外破坏 OAuth/Bearer，也可能产生审计上难以判断的认证来源。

3. **PII 默认面较大**
   全局开关会发送 name、email、role。邮箱和角色应按最小必要原则单独授权，而不是与 opaque user ID 同时默认开启。

4. **明文身份只是声明，不是证明**
   若 MCP Server 对公网开放且允许绕过可信反向代理，攻击者可自行构造同名 Header。TLS 只保护传输，不证明 Header 由 Open WebUI 创建。

5. **签名 JWT 仍有边界**
   当前 JWT 没有 `aud`、`jti`，使用全局 HS256 secret，并且只签用户信息，不签 chat/message。签名失败时源码会回退到明文用户 Header，属于可用性优先的 fail-open 策略。

6. **自定义模板不受 JWT 模式保护**
   即使全局用户身份改为 JWT，管理员配置的 `X-Email: {{USER_EMAIL}}` 仍是明文。

7. **重定向边界不明确**
   MCP httpx client 显式 `follow_redirects=True`。代码没有对跨 origin 重定向主动清除所有业务上下文 Header；前端甚至保留 MCP URL 的尾斜杠以避免 301 丢失认证。高敏感 Header 场景应默认拒绝重定向，至少只允许同 origin。

8. **配置存储与回显**
   自由格式 Header 可能被管理员当作静态 secret 使用；所追踪配置表路径没有字段级加密。UI/接口、备份和运维查询都可能扩大暴露面。

9. **Probe 与真实运行不完全一致**
   Verify 路径不走完整共享构造器，可能出现“验证成功但聊天失败”或相反。

10. **空值与未知 token 容错可能掩盖误配置**
    未知 token 原样发送，缺失 token 变成空字符串。兼容性好，但对身份/路由 Header 来说可能把拼写错误延迟到下游。

## 8. 与 DEEIX Chat 当前实现对照

### 8.1 当前已有能力

本项目已经具备实现该功能的大部分基础设施：

- `backend/internal/infra/persistence/models/mcp.go` 已保存 `HeadersJSON`，Auth Token 单独加密。
- `backend/internal/application/mcp/service.go` 校验 Header JSON 为 `map[string]string`。
- `backend/internal/shared/security/security.go` 对敏感 Header 名回显脱敏。
- `backend/internal/infra/mcp/client.go` 将静态 Header 加到每个 initialize/list/call HTTP 请求。
- `backend/internal/application/conversation/service_tool.go` 已掌握 `UserID`、`ConversationID` 和 `RequestID`。
- MCP `tools/call.params._meta` 已携带上述三个字段。
- 出站 URL 已接入 SSRF 校验与安全 Dialer。
- MCP 工具受管理员配置、状态、单轮选择上限、调用次数、并发、超时和重试治理。

### 8.2 差异表

| 方面 | Open WebUI v0.10.2 | DEEIX Chat 当前 | 需要补齐 |
| --- | --- | --- | --- |
| Header 配置 | JSON dict，支持模板 | `HeadersJSON` 静态字符串 map | 增加运行时模板解析 |
| 用户上下文 | 完整 user 对象 | tool loop 只有数值 UserID | 一次性解析公开用户身份 |
| 会话上下文 | chat、assistant message、user message | 数值 ConversationID，无 message public ID | 传入公开 conversation/message ID |
| Header 范围 | 一轮聊天内所有 MCP HTTP 请求 | 每次 ListTools/CallTool 的所有请求 | 模板解析后同样覆盖完整操作 |
| MCP client 生命周期 | 每轮、每 Server，一个 client 可多次 call | 每个 tool call 重新 initialize | MVP 可保持；后续按 run/server 复用 |
| 协议实现 | SDK 1.27.2，协商 2025-11-25 | 手写 2025-06-18 | 补协议版本头和生命周期测试 |
| `MCP-Protocol-Version` | SDK 在 initialize 后自动发送 | 当前未发送 | 必须修复 |
| session 关闭 | SDK 可 DELETE | 当前没有 DELETE | 建议补齐 |
| SSE | SDK 完整处理与重连 | 当前只提取首个 SSE data block | 与协议升级一起评估 |
| 身份签名 | 可选用户 JWT | 无 | DEEIX MVP 采用逐 Server HS256、精确 `aud` 和短 TTL |
| ID 类型 | 字符串/UUID 风格 | `_meta` 暴露内部 uint | 改为公共 ID |
| Header 冲突 | SDK 保护部分协议头，Authorization 可覆盖 | 自定义 Header 最后写入，可覆盖全部保留头 | 保存时拒绝保留头 |
| 重定向 | MCP client 跟随重定向 | Go `http.Client` 默认跟随重定向 | 明确 same-origin/deny 策略 |

### 8.3 当前协议相关问题

`backend/internal/infra/mcp/client.go` 当前：

- initialize payload 固定请求 `2025-06-18`；
- 后续请求没有按该版规范发送 `MCP-Protocol-Version`；
- 每次 `CallTool` 都重新 initialize；
- 获取到 `Mcp-Session-Id` 后会在本次操作后续请求使用；
- 不发送 DELETE；
- 自定义 Header 在协议、认证、session Header 之后写入，因此能覆盖它们；
- Go `http.Client` 没有自定义 `CheckRedirect`。

动态 Header 功能会扩大出站数据敏感度，所以这些问题应作为前置 hardening 处理，而不是留到功能上线后再修。

## 9. DEEIX Chat 推荐目标设计

### 9.1 设计原则

1. **上下文来源可信**：模板值只能来自认证主体、后端数据库实体和服务端生成的 request/run/trace ID。
2. **公共 ID 优先**：对外发送 user、conversation、message 的 `PublicID`，不发送自增主键。
3. **显式最小披露**：默认只建议 user public ID、conversation public ID、message public ID、request ID；name/email/role 必须由管理员显式配置。
4. **协议头不可配置**：业务模板不能改变 MCP transport 行为。
5. **认证与身份声明分离**：Bearer/OAuth 负责访问 MCP Server；用户 Header 负责业务主体映射。
6. **一处解析，多处复用**：探测、工具同步、对话调用均使用同一个纯函数和合并器。
7. **每操作不可变**：解析后创建新 map，不修改数据库配置 map，不在并发调用间共享可变 Header。
8. **日志零值泄漏**：只记录 Header 名、模板命中情况和上下文是否存在，不记录值。

### 9.2 建议的上下文模型

~~~go
type RequestContext struct {
    UserPublicID         string
    UserDisplayName      string
    UserEmail            string
    UserRole             string
    ConversationPublicID string
    MessagePublicID      string
    UserMessagePublicID  string
    RequestID            string
    RunID                string
    TraceID              string
    Mode                 string // chat, probe, sync
}
~~~

建议 MVP token：

| Token | DEEIX 语义 |
| --- | --- |
| `{{USER_ID}}` | user public ID |
| `{{USER_NAME}}` | DisplayName，空时回退 Username |
| `{{USER_EMAIL}}` | 当前权威邮箱 |
| `{{USER_ROLE}}` | 当前权威角色 |
| `{{CHAT_ID}}` | conversation public ID；兼容 Open WebUI 命名 |
| `{{CONVERSATION_ID}}` | 与 `CHAT_ID` 同义的项目原生命名 |
| `{{MESSAGE_ID}}` | 当前 assistant message public ID |
| `{{USER_MESSAGE_ID}}` | 触发本轮的 user message public ID |
| `{{REQUEST_ID}}` | HTTP/应用 request ID |
| `{{RUN_ID}}` | conversation run public ID |
| `{{TRACE_ID}}` | trace ID，仅在有值时使用 |

用户资料应通过窄接口在一轮运行开始时加载一次并缓存于 run context，不能在每个 tool call 重查数据库。conversation 与 message public ID 在消息持久化后写入同一 context。

### 9.3 建议的组件边界

- `application/conversation`：建立权威 `RequestContext`，决定本轮可用 MCP Server/Tool。
- `application/mcp`：验证管理员 Header 模板、保留头和变量语法；处理 probe/sync context。
- `infra/mcp/header_template.go`：纯函数解析模板和合并 Header，不访问数据库、不依赖 Gin。
- `infra/mcp/client.go`：只负责协议 Header、认证 Header、HTTP 请求和 session；接收已经解析的业务 Header。
- `transport/http/mcp`：DTO、权限、Swagger、错误映射；不在 Handler 中拼 Header。

MVP 可以继续复用数据库的 `HeadersJSON` 字段，不需要 schema 变更。建议把 `CallConfig.Headers` 重命名或拆分为：

~~~go
type CallConfig struct {
    BaseURL        string
    AuthToken      string
    TimeoutMS      int
    HeaderTemplate map[string]string
}
~~~

`CallTool` 或未来的 per-run session 建立入口接收 `RequestContext`，解析一次后在 initialize、initialized、list/call 和 close 中复用。

### 9.4 Header 合并策略

建议严格定义为：

| 层 | 示例 | 是否允许模板覆盖 |
| --- | --- | --- |
| HTTP transport | `Host`、`Content-Length`、`Connection`、`Transfer-Encoding` | 禁止 |
| MCP protocol | `Accept`、`Content-Type`、`MCP-Session-Id`、`MCP-Protocol-Version`、`Last-Event-ID` | 禁止 |
| MCP authentication | `Authorization`、`Cookie` | 默认禁止；通过专门认证配置管理 |
| DEEIX signed context | `X-DEEIX-Context` | 禁止普通模板覆盖 |
| Admin custom headers | `X-Tenant`、`X-User-Id` | 允许模板 |

保存时应使用 `http.CanonicalHeaderKey` 后做大小写不敏感比较，拒绝保留头、伪 Header、空名称、CR/LF 和控制字符。单个名称、值、Header 数量和总字节数都应设上限。

为兼容 Open WebUI，MVP 可采用“已知缺失变量替换为空、未知变量保留原文”的运行时行为，但保存接口必须返回未知 token warning，管理 UI 显示最终示例和 probe/chat 两种预览。对路由关键 Header，可在后续增加 strict 模式。

### 9.5 Probe 与 Sync 语义

- **probe**：可以带发起验证的管理员 user context，但 `CHAT_ID`、`MESSAGE_ID`、`RUN_ID` 必须为空；响应明确展示哪些模板为空。
- **sync**：建议默认使用 system context，不透传管理员身份，避免工具发现结果被某个管理员主体污染；若服务要求用户化发现，应另行设计 per-user discovery，不能把管理员发现结果当作所有用户的全局工具契约。
- **chat**：使用已认证最终用户、当前 conversation/message/run context。

三种模式调用同一个 Header builder，只是输入 context 不同。

### 9.6 DEEIX MVP：每个 MCP Server 独立 HS256 签名上下文

DEEIX MVP 保留两种互不混淆的能力：

- `plain_templates`：管理员显式配置兼容 Header，值可能包含用户/会话模板；下游只能把它当路由或审计信息。
- `signed_context_hs256`：DEEIX 在独立的 `X-DEEIX-Context` Header 中发送可验证的用户与运行上下文 JWT。

每个 MCP Server 生成和保存自己的 HS256 secret，绝不使用全局共享 key，也不与 `AuthTokenEnc`、MCP OAuth client secret、用户登录 JWT、`DataEncryptionKey` 或 `HeadersJSON` 中的静态值复用。推荐的数据模型字段为：

| 字段 | 说明 |
| --- | --- |
| `ContextJWTMode` | `none` 或 `hs256`，为未来算法扩展保留空间 |
| `ContextJWTSecretEnc` | 使用现有 `DataEncryptionKey` 加密后的每 Server secret |
| `ContextJWTAudience` | 必填且稳定的目标 audience |
| `ContextJWTKeyID` | JWT Header 的 `kid`，用于轮换时选择验证密钥 |
| `ContextJWTExpiresSeconds` | 默认 300 秒，限制为安全范围 |
| `ContextJWTPendingSecretEnc` / `ContextJWTPendingKeyID` | 仅在两阶段轮换中短期保存待启用 key；激活或超时后清除 |

`ContextJWTAudience` 建议使用创建 Server 时生成的稳定标识：

~~~text
urn:deeix:mcp:<mcp-server-public-id>
~~~

不建议把可变 URL 直接作为唯一 audience，因为修改域名、路径或反向代理会无意改变安全身份。控制面应把 audience 与 secret 一起展示给 MCP Server 运维者。

#### 密钥生成与控制面

1. 使用 Go `crypto/rand` 生成至少 32 个随机字节。
2. 编码为无填充 Base64URL ASCII 字符串；两侧都把该字符串的 UTF-8 字节作为 HMAC key，不再做隐式 Base64 解码。
3. DEEIX 只在创建或轮换成功响应中展示一次明文 secret。
4. 写入数据库前立即用现有 secretbox/`DataEncryptionKey` 加密。
5. 普通查询响应只返回 `configured: true`、audience、`kid` 和 TTL，不返回 secret，也不返回伪装成真实值的掩码字符串。
6. 生成、更新、轮换和清除操作写审计事件，但审计 details、日志、trace 和系统事件中永不包含 secret 或 JWT。

建议使用独立控制面操作，而不是把 secret 混入通用 Server Update。为避免“生成新 key 后立刻切换，MCP Server 尚未来得及配置”的竞态，轮换采用 prepare/activate 两阶段：

~~~text
POST /api/v1/admin/mcp/servers/{id}/context-jwt/rotations
POST /api/v1/admin/mcp/servers/{id}/context-jwt/rotations/{kid}/activate
DELETE /api/v1/admin/mcp/servers/{id}/context-jwt/rotations/{kid}
DELETE /api/v1/admin/mcp/servers/{id}/context-jwt
~~~

`POST .../rotations` 生成 pending key、加密保存，并返回一次性的：

~~~json
{
  "header": "X-DEEIX-Context",
  "algorithm": "HS256",
  "secret": "<one-time-value>",
  "issuer": "https://chat.example.com",
  "audience": "urn:deeix:mcp:mcp_xxx",
  "keyID": "ctx_20260710_xxx",
  "expiresSeconds": 300
}
~~~

管理员必须把 `secret`、`issuer`、`audience` 和 `keyID` 交给 MCP Server 的 Secret Manager 配置；确认目标 Server 已接受 pending key 后，再调用 `activate` 原子提升为 current key。未激活的 pending key 应有控制面超时并可显式取消。DEEIX 不会在 MCP initialize、OAuth discovery、工具参数或其他网络消息中发送 secret。

#### 签发内容

JWT Header 固定：

~~~json
{
  "alg": "HS256",
  "typ": "JWT",
  "kid": "ctx_20260710_xxx"
}
~~~

JWT claims 建议为：

~~~json
{
  "iss": "https://chat.example.com",
  "aud": "urn:deeix:mcp:mcp_xxx",
  "sub": "user-public-id",
  "name": "display-name",
  "email": "user@example.com",
  "role": "user",
  "conversation_id": "conversation-public-id",
  "message_id": "assistant-message-public-id",
  "user_message_id": "user-message-public-id",
  "request_id": "request-id",
  "run_id": "run-id",
  "iat": 0,
  "nbf": 0,
  "exp": 0,
  "jti": "assertion-id"
}
~~~

`sub`、conversation、message、request 和 run 使用服务端权威上下文与公开 ID。name、email、role 属于 PII/权限提示，只有管理员在该 Server 上显式允许时才加入；即使包含 role，MCP Server 也应把它作为已签名属性而不是无限制管理员授权。

在当前“每次 `CallTool` 新建一次 MCP session”的实现中，每次操作签发一个 JWT，并在 initialize、initialized、tools/call 和 close 中复用即可。若未来改为长生命周期 per-run session，应在每个出站 HTTP 请求前基于不可变 `RequestContext` 重新签发，避免运行时间超过 TTL 后出现半程 401。

#### MCP Server 验证责任

MCP Server 必须在处理 JSON-RPC body 前：

1. 从 `X-DEEIX-Context` 读取 JWT；签名模式启用时，缺失即拒绝。
2. 依据 `kid` 选择当前或轮换期旧 key。
3. 强制算法为 `HS256`，不能接受 JWT Header 自选算法或 `none`。
4. 验证签名、精确 `iss`、精确 `aud`、`exp`、`nbf` 和 `iat`，只允许有限时钟偏差。
5. 验证成功后才把 `sub` 和业务 claims 注入 MCP request context。
6. JWT 缺失、格式或签名错误返回 401；身份有效但 audience/业务权限不足返回 403。
7. 不记录原始 JWT；日志只记录 `kid`、验证结果和安全的 request ID。

`Authorization: Bearer ...` 与 `X-DEEIX-Context` 可以同时存在：前者证明 DEEIX Client 有权连接 MCP Server，后者证明本次调用代表哪个最终用户与会话。两者不得互相替代。

#### 多副本与轮换

- 所有 DEEIX 副本从共享数据库读取同一条已加密的 Server secret，并使用相同 `DataEncryptionKey` 解密。
- 所有目标 MCP Server 副本从其 Secret Manager 读取同一验证 secret。
- 跨 Server 不共享 secret；即使误共享，精确 `aud` 校验也必须阻止跨 Server replay。

MVP 采用 prepare/activate 和 MCP Server 双钥验证窗口完成无停机轮换：

1. 调用 `POST .../rotations`，由 DEEIX 生成并加密保存 pending secret，只在响应中回显一次。
2. 把新 secret/`kid` 写入目标 MCP Server 的 Secret Manager，使其同时接受旧、新 `kid`。
3. 调用 `activate`，由 DEEIX 原子提升 pending key 并开始只用新 key 签名；旧 key 不再由 DEEIX 保存或用于签名。
4. 等待 `最大 JWT TTL + 最大时钟偏差 + 在途请求上限`。
5. 从 MCP Server 删除旧 key，并销毁 Secret Manager 的旧版本。

若 MCP Server 不支持双钥验证，只能采用协调切换并接受短暂不可用。pending key 必须设置准备超时并支持取消；MVP 不在 DEEIX 中长期保存 previous secret，回滚与历史 key 版本由 Secret Manager 管理。

该方案的安全收益是把泄漏半径限制在单个 MCP Server。被攻陷的 Server 仍能用自己的对称 key 为自己伪造身份，这是 HS256 的固有限制；但它不能为其他使用独立 key 的 Server 生成有效 JWT。非对称签名和 JWKS 可作为后续增强，不属于本次 MVP。

### 9.7 重定向和 SSRF

现有安全 Dialer 能限制目标 IP，但动态 PII Header 还需要重定向策略：

- 默认 `CheckRedirect = http.ErrUseLastResponse`；
- 如果必须允许，只允许 scheme、host、有效端口均相同的 same-origin 重定向；
- 每次重定向重新做 URL/SSRF 校验；
- 跨 origin 绝不复制 Authorization、Cookie、签名上下文或任何含用户/会话信息的自定义 Header；
- 管理 UI 保存规范化后的最终 MCP endpoint，减少尾斜杠重定向。

### 9.8 Session 隔离

当前 DEEIX 每个 `CallTool` 新建 session，性能一般但不会跨用户共享。若后续改为 Open WebUI 式 per-run session：

- key 至少包含 server ID、user public ID、conversation/run ID、认证主体；
- 不得按 server URL 全局复用带默认 Header 的 client；
- 同一 session 的 Header context 必须不可变；
- 本轮结束、取消、超时和错误路径都必须 close/DELETE；
- 重试要明确是复用同一 session 还是重新 initialize，避免非幂等工具重复执行。

## 10. 实施顺序

### 阶段 A：协议与安全前置

1. 为后续请求补 `MCP-Protocol-Version`。
2. 禁止自定义 Header 覆盖协议、Hop-by-hop 与认证保留头。
3. 明确重定向策略。
4. 补 session DELETE/关闭逻辑。
5. 用 `httptest.Server` 捕获 initialize、initialized、tools/list、tools/call 的完整请求序列。

### 阶段 B：模板 MVP

1. 增加纯 Header 模板解析器。
2. 建立一次性 `RequestContext`，使用公共 ID。
3. 将 `HeadersJSON` 解释为模板；无数据库迁移。
4. chat/probe/sync 共用 Header builder。
5. 保留 `tools/call._meta`，但改用公共 ID 并补 message/run/request。
6. 更新 Swagger、前端手写类型、管理 UI token 帮助与中英文文案。

### 阶段 C：可信身份与治理

1. 实现每 MCP Server 独立 HS256 key、精确 `aud`、`kid` 和短期 JWT 的 signed context。
2. name/email/role 字段级 opt-in。
3. 实现 secret 一次性回显、`DataEncryptionKey` 加密落库和 prepare/activate 双阶段轮换。
4. Header 模板预览、未知 token warning、strict 模式。
5. 审计模板和签名配置变更，但永不记录 Header、secret 或 JWT 值。

### 阶段 D：生命周期优化

1. 按 run/server 复用 MCP client。
2. 完整 SSE、分页 `tools/list`、404 session 重建和取消。
3. 协议版本可升级，并持续跟踪 MCP stateless draft。

## 11. 测试与验收清单

### 11.1 单元测试

- 每个 token 的正常替换、组合替换、重复替换。
- 缺失上下文、未知 token、空 Header、Unicode 值。
- CR/LF、控制字符、超长值、非法 Header 名拒绝。
- 保留头大小写变体拒绝，例如 `mcp-session-id`。
- 输入 map 不被修改，并发解析无数据竞争。
- probe/sync/chat 三种 context 的空值语义。
- 敏感 Header 回显和日志脱敏。
- HS256 secret 生成长度、Base64URL 表示、加密往返和错误主密钥解密失败。
- JWT 固定为 HS256，并包含正确 `kid`、`iss`、精确 `aud`、短 `exp` 和唯一 `jti`。

### 11.2 MCP 传输集成测试

- initialize 请求收到正确业务 Header。
- initialized、tools/list、tools/call 收到相同业务 Header。
- 后续请求带服务端签发的 `MCP-Session-Id` 和协商后的 `MCP-Protocol-Version`。
- 自定义模板不能覆盖标准 Header。
- 非聊天 probe 的 chat/message Header 空或省略，符合约定。
- DELETE 关闭请求仍带协议 session，但不泄漏到其他 origin。
- JSON 与 SSE 响应路径都通过。

### 11.3 隔离和安全测试

- 两个用户并发调用同一 Server，Header 不串值。
- 同一用户的两个会话、两条消息不串值。
- 失败、取消、超时、重试后 session 均清理。
- 302 到同 origin/跨 origin 的行为符合策略。
- SSRF 目标和重定向目标均受校验。
- 任何日志、trace、系统事件、LastError 均不包含 Header 值、JWT、用户邮箱。
- 签名模式校验 `iss`、`aud`、`exp`、`jti`，签名失败不降级为明文可信身份。
- Server A 的 JWT 发送给 Server B 时，即使误配了相同 secret，也因 `aud` 不匹配而拒绝。
- 管理查询和普通更新接口不返回明文或可逆的 secret；只有生成 pending key 的响应回显一次。
- prepare 后未 activate 不改变当前签名 key；activate 原子切换，取消/超时清除 pending key。
- 轮换窗口内 MCP Server 接受旧、新 `kid`；窗口结束后旧 key 和旧 token 均被拒绝。
- 多个 DEEIX/MCP Server 副本分别使用一致的 `DataEncryptionKey`/验证 key，不出现随机签名失败。

### 11.4 兼容性验收

- 现有纯静态 `HeadersJSON` 行为不变。
- 未使用模板的 MCP Server 无需重新配置。
- 管理员验证连接与真实聊天使用相同认证/模板规则。
- 前端不产生权限、身份或 Header 最终值，只提交配置。
- 跨端 API 变更同步 handler DTO、Swagger、前端类型和调用点。

## 12. 最终建议

DEEIX Chat 可以在现有架构上以较低迁移成本实现类似功能：`HeadersJSON`、MCP 调用上下文、敏感 Header 脱敏和 SSRF 基础都已存在。真正的工作重点不是字符串替换，而是建立可信的公开 ID 上下文、规定 Header 所有权与合并优先级，并确保完整 MCP 生命周期内一致且不跨用户泄漏。

推荐的上线门槛是：

1. 先完成协议版本头、保留头和重定向 hardening；
2. 再上线连接级模板，默认只鼓励 opaque public ID；
3. 邮箱、角色和可信授权采用显式 opt-in；
4. 需要下游据此授权时，使用每 MCP Server 独立 HS256 key、精确 `aud`、`kid` 和短 TTL 的 signed context；
5. 不透传用户登录 Token，不让 `CHAT_ID` 冒充 `MCP-Session-Id`，不把业务 Header 当作 MCP 标准。

Open WebUI 最值得复用的是“后端运行时展开、每轮隔离、覆盖完整握手”的思路；最需要改进的是“多路径行为漂移、Header 冲突、全局 PII 和签名 fail-open”。按本报告的阶段顺序实施，可以在兼容常见 MCP Server 的同时，避免把一个便利功能演变成跨租户身份泄漏面。
