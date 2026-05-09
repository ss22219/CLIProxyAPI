# Kiro (AWS Q) API 完整参考文档

> 通过 mitmproxy 抓包 kiro-cli 2.0.1 逆向获得，2026-04-22 初版；**2026-05-08 基于 2.2.2（Windows, Social/Google 登录）重新验证并增补**。
>
> 变更摘要（2026-05-08）：
> - 新增 §13 启动时版本检查（`desktop-release.q.*.amazonaws.com/latest/manifest.json`）
> - §7 增补 `claude-opus-4.7` 及 `additionalModelRequestFieldsSchema`（thinking / output_config.effort）
> - §9 Social Refresh 实测更正：UA、Accept、HTTP/2、响应多一个 `profileArn`、**走 HTTPS_PROXY**
> - §12 Toolkit Telemetry 依然在 2.2.2 中活跃，补齐 4 个 metric 的真实字段集
> - §2 凭证存储表按 Social / SSO 两种登录方式拆开

## 目录

1. [概述](#1-概述)
2. [认证](#2-认证)
3. [API 列表](#3-api-列表)
4. [GenerateAssistantResponse — 聊天](#4-generateassistantresponse)
5. [SendTelemetryEvent — 遥测](#5-sendtelemetryevent)
6. [GetProfile — 获取用户配置](#6-getprofile)
7. [ListAvailableModels — 获取可用模型](#7-listavailablemodels)
8. [OIDC Token Refresh — SSO 令牌刷新](#8-oidc-token-refresh)
9. [Social Token Refresh — 社交登录令牌刷新](#9-social-token-refresh)
10. [多模态支持（图片）](#10-多模态支持)
11. [响应流事件格式](#11-响应流事件格式)
12. [client-telemetry（Toolkit 遥测）](#12-client-telemetry)
13. [启动时版本检查 manifest.json](#13-启动时版本检查)

---

## 1. 概述

Kiro API 基于 AWS CodeWhisperer 服务，通过 `x-amz-target` header 区分不同操作。

| 服务 | 基础 URL | 认证方式 |
|------|----------|----------|
| Q API（聊天/遥测/配置） | `https://q.{region}.amazonaws.com/` | Bearer Token |
| OIDC（SSO 令牌刷新） | `https://oidc.{region}.amazonaws.com/token` | Client Credentials |
| Social（社交登录刷新） | `https://prod.{region}.auth.desktop.kiro.dev/refreshToken` | Refresh Token |
| Toolkit Telemetry | `https://client-telemetry.{region}.amazonaws.com/metrics` | AWS SigV4 |
| 版本清单 | `https://desktop-release.q.{region}.amazonaws.com/latest/manifest.json` | 无 |

---

## 2. 认证

### Bearer Token

所有 Q API 请求使用 Bearer Token：
```
authorization: Bearer {accessToken}
```

Token 格式：`aoaAAAAA...`（以 `aoa` 开头），有效期约 1 小时。

### 凭证存储

kiro-cli 凭证存储在 SQLite 数据库中：
```
%LOCALAPPDATA%\Kiro-Cli\data.sqlite3
```

登录方式**通过 `auth_kv` 表里的 key 判定**——`odic` 前缀是 SSO，`social` 前缀是社交登录（Google / GitHub 等），两种登录互斥。

#### SSO 登录（AWS IAM Identity Center）

| 表 | Key | value 结构 |
|----|-----|------------|
| `auth_kv` | `kirocli:odic:device-registration` | `{ client_id, client_secret, region, ... }`（JWT 格式的 client_secret，约 3800 chars） |
| `auth_kv` | `kirocli:odic:token` | `{ access_token, refresh_token, expires_at, region, ... }` |

> key 的拼写真的是 `odic`（不是 `oidc`），这是 kiro-cli 内部的拼写。

#### 社交登录（Google / GitHub 等）

| 表 | Key | value 结构 |
|----|-----|------------|
| `auth_kv` | `kirocli:social:token` | `{ access_token, refresh_token, expires_at, provider, profile_arn }` |

实际示例值：
```json
{
  "access_token":  "aoaAAAAA...",          // 232 chars
  "refresh_token": "aorAAAAA...",          // 232 chars
  "expires_at":    "2026-05-08T02:44:55.8835694Z",
  "provider":      "google",
  "profile_arn":   "arn:aws:codewhisperer:us-east-1:<accountId>:profile/<profileId>"
}
```

> 注意这里是 **`profile_arn`（snake_case）**，而 API 响应中的字段名是 `profileArn`（camelCase）。

#### state 表（两种登录共用）

| Key | 内容 |
|-----|------|
| `telemetryClientId` | 固定 UUID（跨会话不变），发给 Q API SendTelemetryEvent 用 |
| `telemetry-cognito-identity-id` | Cognito identity id，作为 SigV4 身份 |
| `telemetry-cognito-credentials` | STS 临时凭证（client-telemetry SigV4 签名用） |
| `telemetry.lastHeartbeatDate` | 上一次心跳时间 |
| `api.codewhisperer.profile` | 缓存的 GetProfile 返回值：`{ arn, profile_name }`；social 登录时 profile_name 是 `Social_Default_Profile`，SSO 时是 `QDefaultProfile` |

---

## 3. API 列表

| API | x-amz-target | UA api 标识 | 用途 |
|-----|-------------|-------------|------|
| GenerateAssistantResponse | `AmazonCodeWhispererStreamingService.GenerateAssistantResponse` | `codewhispererstreaming` | 聊天（流式） |
| SendTelemetryEvent | `AmazonCodeWhispererService.SendTelemetryEvent` | `codewhispererruntime` | 遥测上报 |
| GetProfile | `AmazonCodeWhispererService.GetProfile` | `codewhispererruntime` | 获取用户配置 |
| ListAvailableModels | `AmazonCodeWhispererService.ListAvailableModels` | `codewhispererruntime` | 获取可用模型 |

### 公共 Headers（所有 Q API 共享）

所有发往 `q.{region}.amazonaws.com` 的请求共享以下 headers：

```http
content-type: application/x-amz-json-1.0
authorization: Bearer {accessToken}
x-amzn-codewhisperer-optout: false
amz-sdk-invocation-id: {uuid}
amz-sdk-request: attempt=1; max=3
accept: */*
accept-encoding: gzip
host: q.us-east-1.amazonaws.com
```

`user-agent` 和 `x-amz-user-agent` 根据 API 类型不同：

| API 类型 | user-agent | x-amz-user-agent |
|----------|-----------|------------------|
| Streaming（GenerateAssistantResponse） | `aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererstreaming/0.1.14474 os/{os} lang/rust/1.92.0 exec-env/AmazonQ-For-CLI Version/2.0.1 md/appVersion-2.0.1 app/AmazonQ-For-CLI` | `...api/codewhispererstreaming/... m/F app/AmazonQ-For-CLI` |
| Runtime（其他所有） | `aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererruntime/0.1.14474 os/{os} lang/rust/1.92.0 exec-env/AmazonQ-For-CLI Version/2.0.1 md/appVersion-2.0.1 app/AmazonQ-For-CLI` | `...api/codewhispererruntime/... m/F app/AmazonQ-For-CLI` |

> `{os}` = `windows` \| `linux` \| `macos`
>
> ListAvailableModels 的 `x-amz-user-agent` 末尾是 `m/F,C`（多了 `,C`），其他 runtime API 是 `m/F`

### ⚠️ Content-Type 注意

必须是 `application/x-amz-json-1.0`，**不能**带 `; charset=utf-8`（.NET `StringContent` 默认会加，需要用 `ByteArrayContent` 规避）。

---

## 4. GenerateAssistantResponse

### 请求

```http
POST https://q.us-east-1.amazonaws.com/
content-type: application/x-amz-json-1.0
x-amz-target: AmazonCodeWhispererStreamingService.GenerateAssistantResponse
authorization: Bearer {accessToken}
user-agent: aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererstreaming/0.1.14474 os/{os} lang/rust/1.92.0 exec-env/AmazonQ-For-CLI Version/2.0.1 md/appVersion-2.0.1 app/AmazonQ-For-CLI
x-amz-user-agent: aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererstreaming/0.1.14474 os/{os} lang/rust/1.92.0 exec-env/AmazonQ-For-CLI Version/2.0.1 m/F app/AmazonQ-For-CLI
x-amzn-codewhisperer-optout: false
amz-sdk-invocation-id: {uuid}
amz-sdk-request: attempt=1; max=3
accept: */*
accept-encoding: gzip
```

`{os}` = `windows` | `linux` | `macos`

### 请求 Body

```jsonc
{
  "conversationState": {
    "conversationId": "{uuid}",           // 同一会话内保持不变
    "chatTriggerType": "MANUAL",
    "agentContinuationId": "{uuid}",      // 每次请求新生成
    "agentTaskType": "vibe",
    "history": [                          // 可选，首轮无 history
      // 用户消息（history 中无 origin）
      { "userInputMessage": {
          "content": "用户文本",
          "userInputMessageContext": {
            "envState": { "operatingSystem": "windows", "currentWorkingDirectory": "C:\\..." }
          }
      }},
      // 助手回复（无 tool call 时无 messageId）
      { "assistantResponseMessage": {
          "content": "助手文本"
      }},
      // 助手回复（有 tool call 时有 messageId）
      { "assistantResponseMessage": {
          "content": "",
          "messageId": "{uuid}",
          "toolUses": [{
            "toolUseId": "tooluse_xxx",
            "name": "fs_read",
            "input": { "operations": [{"mode": "Line", "path": "..."}] }
          }]
      }},
      // Tool result 回传
      { "userInputMessage": {
          "content": "",
          "userInputMessageContext": {
            "envState": { "operatingSystem": "windows", "currentWorkingDirectory": "C:\\..." },
            "toolResults": [{
              "toolUseId": "tooluse_xxx",
              "content": [{ "text": "工具输出文本" }],
              "status": "success"
            }]
          }
      }}
    ],
    "currentMessage": {
      "userInputMessage": {
        "content": "当前用户消息",
        "origin": "KIRO_CLI",             // ⚠️ 只在 currentMessage 上
        "modelId": "auto",                // 或 "claude-opus-4.6" 等
        "images": [{                      // 可选，有图片时
          "format": "png",
          "source": { "bytes": "iVBORw0KGgo..." }  // base64
        }],
        "userInputMessageContext": {
          "envState": { "operatingSystem": "windows", "currentWorkingDirectory": "C:\\..." },
          "tools": [{ "toolSpecification": {
            "name": "fs_read",
            "description": "Read lines from a file",
            "inputSchema": { "json": { "type": "object", "properties": {...}, "required": [...] } }
          }}],
          "toolResults": [{               // 可选，tool result 回传时
            "toolUseId": "tooluse_xxx",
            "content": [{ "text": "工具输出" }],
            "status": "success"
          }]
        }
      }
    }
  },
  "profileArn": "arn:aws:codewhisperer:us-east-1:XXXX:profile/XXXX"
}
```

### 关键规则

| 规则 | 说明 |
|------|------|
| `origin` | 只在 `currentMessage.userInputMessage` 上，history 中不带 |
| `modelId` | 只在 `currentMessage.userInputMessage` 上 |
| `messageId` | `assistantResponseMessage` 中只在有 `toolUses` 时才有 |
| `tools` | 每次请求的 `currentMessage` 中都带完整 tool 列表 |
| `content` | tool result 回传时 content 为空字符串 `""` |
| `conversationId` | 同一会话内保持不变 |
| `images` | 在 `currentMessage.userInputMessage` 级别，不在 `toolResults` 中 |

### 响应

二进制 event stream，详见 [第 11 节](#11-响应流事件格式)。

---

## 5. SendTelemetryEvent

### 请求

```http
POST https://q.us-east-1.amazonaws.com/
content-type: application/x-amz-json-1.0
x-amz-target: AmazonCodeWhispererService.SendTelemetryEvent
user-agent: aws-sdk-rust/1.3.14 ua/2.1 api/codewhispererruntime/0.1.14474 os/{os} ...
```

注意 UA 中是 `codewhispererruntime`（不是 streaming）。

### 请求 Body

```json
{
  "clientToken": "{uuid}",
  "telemetryEvent": {
    "chatAddMessageEvent": {
      "conversationId": "{与 chat 请求相同的 conversationId}",
      "messageId": "{uuid}",
      "timeToFirstChunkMilliseconds": 3173.458,
      "timeBetweenChunks": [0.0165, 0.0061, 0.0026, ...],
      "responseLength": 71
    }
  },
  "optOutPreference": "OPTIN",
  "userContext": {
    "ideCategory": "CLI",
    "operatingSystem": "WINDOWS",
    "product": "CodeWhisperer",
    "clientId": "{固定 uuid，跨会话不变}",
    "ideVersion": "2.0.1"
  },
  "profileArn": "arn:aws:codewhisperer:...",
  "modelId": "auto"
}
```

| 字段 | 说明 |
|------|------|
| `timeToFirstChunkMilliseconds` | 从请求发送到收到第一个 content 事件的毫秒数 |
| `timeBetweenChunks` | 相邻 content 事件之间的间隔（秒） |
| `responseLength` | 响应文本总字符数 |
| `operatingSystem` | **大写**：`WINDOWS` / `LINUX` / `MACOS` |
| `clientId` | 持久化的 UUID，存储在 sqlite `state` 表 `telemetryClientId` |

### 响应

```
200 OK (空 body)
```

---

## 6. GetProfile

### 请求

```http
POST https://q.us-east-1.amazonaws.com/
x-amz-target: AmazonCodeWhispererService.GetProfile
user-agent: ...api/codewhispererruntime/...
```

```json
{
  "profileArn": "arn:aws:codewhisperer:us-east-1:XXXX:profile/XXXX"
}
```

> 2026-05-09 本地 SSO/OIDC 凭证实测：`profileArn` 不能通过空 body 的 GetProfile 探测。请求 `{}` 会返回
> `ValidationException: Invalid profileArn.`；请求 `{"profileArn":""}` 会返回 ARN 正则校验错误。
> 若本地 `state` 表没有 `api.codewhisperer.profile`，`kirocli:odic:*` 两个 auth_kv 记录本身不包含 profile ARN。

### 响应

```json
{
  "profile": {
    "arn": "arn:aws:codewhisperer:us-east-1:XXXX:profile/XXXX",
    "profileName": "QDefaultProfile",
    "profileType": "Q_DEVELOPER",
    "status": "ACTIVE",
    "optInFeatures": { "dashboardAnalytics": { "toggle": "ON" } },
    "referenceTrackerConfiguration": { "recommendationsWithReferences": "ALLOW" }
  }
}
```

---

## 7. ListAvailableModels

### 请求

```http
POST https://q.us-east-1.amazonaws.com/?origin=KIRO_CLI&profileArn={encoded_arn}
x-amz-target: AmazonCodeWhispererService.ListAvailableModels
```

```json
{
  "origin": "KIRO_CLI",
  "profileArn": "arn:aws:codewhisperer:..."
}
```

> 2026-05-09 本地 SSO/OIDC 凭证 + mitmproxy 实测：使用 Kiro 2.2.2 runtime headers 时，省略
> `profileArn`（仅带 `origin=KIRO_CLI`）会返回 `ValidationException: Invalid profileArn.`，因此模型列表不能作为
> profile ARN 的发现入口。若去掉 Kiro CLI 的 UA / `x-amz-user-agent` 等特征 header，服务端可能返回一份通用模型目录，
> 但这不等同于 Kiro CLI 真实调用路径。
> 当前返回模型 ID：`auto`, `claude-opus-4.7`, `claude-opus-4.6`, `claude-sonnet-4.6`,
> `claude-opus-4.5`, `claude-sonnet-4.5`, `claude-sonnet-4`, `claude-haiku-4.5`,
> `deepseek-3.2`, `minimax-m2.5`, `minimax-m2.1`, `glm-5`, `qwen3-coder-next`。

### BuilderId/OIDC 默认 profile ARN

2026-05-09 mitmproxy 抓 `kiro-cli 2.2.2 chat --list-models`：本地 SQLite、`~/.aws`、`~/.kiro`
均未保存 profile ARN，OIDC token/JWT payload 也不包含它；但 CLI 发出的 `ListAvailableModels`
请求在 query 和 body 中都携带固定 BuilderId profile ARN：

```text
arn:aws:codewhisperer:us-east-1:638616132270:profile/AAAACCCCXXXX
```

因此对 BuilderId/OIDC 导入，若 social token 或 `state.api.codewhisperer.profile` 没有提供 profile ARN，
应使用该默认 ARN；社交登录仍以 `kirocli:social:token.profile_arn` 或 refresh 响应 `profileArn` 为准。

### 响应

```json
{
  "defaultModel": { "modelId": "auto" },
  "models": [
    {
      "modelId": "auto",
      "modelName": "auto",
      "description": "Models chosen by task for optimal usage and consistent quality",
      "rateMultiplier": 1.0,
      "rateUnit": "Credit",
      "supportedInputTypes": ["TEXT", "IMAGE"],
      "tokenLimits": { "maxInputTokens": 1000000, "maxOutputTokens": 64000 },
      "promptCaching": {
        "supportsPromptCaching": true,
        "maximumCacheCheckpointsPerRequest": 4,
        "minimumTokensPerCacheCheckpoint": 1024
      }
    },
    {
      "modelId": "claude-opus-4.6",
      "modelName": "Claude Opus 4.6",
      "description": "The Claude Opus 4.6 model",
      "rateMultiplier": 15.0,
      "supportedInputTypes": ["TEXT", "IMAGE"],
      "tokenLimits": { "maxInputTokens": 200000, "maxOutputTokens": 64000 }
    }
  ]
}
```

### 2.2.2 新增：`claude-opus-4.7` 与 `additionalModelRequestFieldsSchema`

> 2026-05-08 抓包 2.2.2 观察到 `auto` 模型之后新增了 `claude-opus-4.7`，并带有 `additionalModelRequestFieldsSchema`，允许请求中追加 `thinking` / `output_config` 扩展字段：

```jsonc
{
  "modelId": "claude-opus-4.7",
  "modelName": "claude-opus-4.7",
  "description": "Experimental preview of Claude Opus 4.7 model with 1M context window",
  "rateMultiplier": 2.2,
  "rateUnit": "Credit",
  "supportedInputTypes": ["TEXT", "IMAGE"],
  "tokenLimits": { "maxInputTokens": 1000000, "maxOutputTokens": 64000 },
  "promptCaching": {
    "supportsPromptCaching": true,
    "maximumCacheCheckpointsPerRequest": 4,
    "minimumTokensPerCacheCheckpoint": 4096
  },
  "additionalModelRequestFieldsSchema": {
    "type": "object",
    "properties": {
      "thinking": {
        "type": "object",
        "properties": {
          "type":    { "type": "string", "enum": ["adaptive", "disabled"] },
          "display": { "type": "string", "enum": ["summarized", "omitted"] }
        },
        "required": ["type"]
      },
      "output_config": {
        "type": "object",
        "properties": {
          "effort": { "type": "string", "enum": ["low", "medium", "high", "xhigh", "max"] }
        }
      }
    },
    "additionalProperties": false
  }
}
```

> 部分老模型（如 sonnet 系列）返回的 schema 只有 `thinking.type` + `output_config.effort`，没有 `display` 字段或 `xhigh` 等级。

---

## 8. OIDC Token Refresh — SSO 令牌刷新

用于 SSO 方式登录的用户刷新 token。这是 AWS SSO OIDC `CreateToken` API（2026-04-22 抓包验证）。

### 请求

```http
POST https://oidc.{region}.amazonaws.com/token
Content-Type: application/json
```

> 这是 AWS SSO OIDC API，**不是**标准 OIDC。使用 **JSON body**（不是 form-urlencoded），字段名是 **camelCase**。

```json
{
  "clientId": "{device_registration.client_id}",
  "clientSecret": "{device_registration.client_secret}",
  "grantType": "refresh_token",
  "refreshToken": "{token.refresh_token}"
}
```

| 参数 | 来源 | 说明 |
|------|------|------|
| `clientId` | sqlite `device-registration` -> `client_id` | 设备注册时获取 |
| `clientSecret` | sqlite `device-registration` -> `client_secret` | JWT 格式，约 3800 chars |
| `grantType` | 固定值 | `refresh_token` |
| `refreshToken` | sqlite `token` -> `refresh_token` | `aor` 开头 |

### 响应

```http
HTTP/1.1 200 OK
Content-Type: application/json
x-amzn-RequestId: {uuid}
```

```json
{
  "accessToken": "aoaAAAAA...",
  "expiresIn": 3600,
  "refreshToken": "aorAAAAA...",
  "tokenType": "Bearer",
  "idToken": null,
  "aws_sso_app_session_id": null,
  "issuedTokenType": null,
  "originSessionId": null
}
```

**注意**：
- `refresh_token` 可重复使用，刷新后返回的新 refresh_token 也有效，新旧均可用
- kiro-cli 的 OIDC refresh 请求**不走 HTTP 代理**

---

| 字段 | 说明 |
|------|------|
| `accessToken` | 新的 Bearer Token（camelCase） |
| `expiresIn` | 有效期（秒），通常 3600 |
| `refreshToken` | 可能返回相同的 refresh token（不一定更新） |
| `tokenType` | 固定 `Bearer` |

### 注意事项

- **不使用 Bearer Token 认证**，认证信息在 body 中
- kiro-cli 的此请求**不走 HTTP 代理**（Rust HTTP 客户端不读 HTTPS_PROXY）
- `refresh_token` 可以重复使用（实测同一个 refresh token 多次刷新均成功）
- 错误码：`invalid_client`（client 凭证错误）、`invalid_grant`（refresh token 无效）、`expired_token`（token 过期）

---
## 9. Social Token Refresh（社交登录）

用于 Google / GitHub 等社交登录的 token 刷新。**2026-05-08 基于 kiro-cli 2.2.2（Google 登录）实抓验证。**

### 请求

```http
POST https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken HTTP/2.0
content-type: application/json
user-agent: Kiro-CLI
accept: */*
accept-encoding: gzip
content-length: 251
```

```json
{
  "refreshToken": "aorAAAAA..."
}
```

| 细节 | 说明 |
|------|------|
| HTTP 版本 | **HTTP/2.0**（CloudFront 前端） |
| 认证 | 不使用 Bearer、不使用 SigV4，仅 body 内的 `refreshToken` |
| `user-agent` | 字面量 **`Kiro-CLI`**（不同于其他 API 用的 `aws-sdk-rust/...`） |
| `accept` | 实测为 `*/*`，**没有**显式 `application/json` |
| Body 字段 | 只有 `refreshToken`，无 clientId / clientSecret / grantType |
| 代理 | **走 `HTTPS_PROXY`**（mitmproxy 能截获；与 §8 OIDC 备注的 "不走代理" 不同） |
| region | 实测固定 `us-east-1`，与 sqlite 里的 region 字段无关 |

### 响应

```http
HTTP/2.0 200 OK
content-type: application/json
content-length: 598
x-amzn-requestid: 1a89b3bf-29e6-4566-b347-55ca205cd28c
x-cache: Miss from cloudfront
via: 1.1 ...cloudfront.net (CloudFront)
```

```json
{
  "accessToken":  "aoaAAAAA...",
  "expiresIn": 3600,
  "profileArn": "arn:aws:codewhisperer:us-east-1:<accountId>:profile/<profileId>",
  "refreshToken": "aorAAAAA..."
}
```

| 字段 | 说明 |
|------|------|
| `accessToken` | 新的 Bearer Token，232 chars，`aoa` 前缀 |
| `expiresIn` | 固定 `3600`（秒） |
| `profileArn` | **响应独有字段**（OIDC 版响应没有），直接带出 CodeWhisperer profile ARN，调用端可跳过 GetProfile 直接缓存 |
| `refreshToken` | 实测**与请求中传入的值完全相同**——social refresh 不轮换 refresh_token（OIDC 版可能会返回新值） |

### 字段命名对比 OIDC

| | OIDC (§8) | Social (§9) |
|-|-----------|-------------|
| 请求字段名 | `clientId` / `clientSecret` / `grantType` / `refreshToken`（camelCase） | 仅 `refreshToken` |
| 响应字段名 | `accessToken` / `expiresIn` / `refreshToken` / `tokenType` / `idToken` | `accessToken` / `expiresIn` / `profileArn` / `refreshToken`（无 tokenType / idToken） |
| UA | aws-sdk-rust/... | 字面量 `Kiro-CLI` |
| 走代理？ | 原 doc 断言不走（未在 2.2.2 复现验证） | **走** |

### 触发条件

kiro-cli 每次进程启动时读 sqlite 中 `kirocli:social:token.expires_at`；如当前时间 ≥ `expires_at`，在首个 Q API 请求前同步调用此 endpoint 换新 token，并把新 `accessToken` / `expiresIn`（换算成 `expires_at`，UTC ISO 时间）写回 sqlite。同一个 `refreshToken` 可以重复使用（实测同一个 refresh_token 换新 token 多次均成功）。

---

## 10. 多模态支持

### 支持的输入类型

`ListAvailableModels` 返回 `supportedInputTypes: ["TEXT", "IMAGE"]`。

### 图片传递方式

图片通过 `currentMessage.userInputMessage.images` 数组传递：

```json
{
  "userInputMessage": {
    "content": "描述这张图片",
    "images": [
      {
        "format": "png",
        "source": {
          "bytes": "iVBORw0KGgoAAAANSUhEUgAA..."
        }
      }
    ],
    "origin": "KIRO_CLI",
    "modelId": "auto"
  }
}
```

| 字段 | 说明 |
|------|------|
| `format` | `png` / `jpeg` / `gif` / `webp` |
| `source.bytes` | base64 编码的图片原始字节 |

### 图片来源

1. **用户直接输入**：用户消息中的 `DataContent`
2. **Tool 返回**：`fs_read` 的 `Image` 模式返回图片数据

两种来源的图片都放在 `currentMessage.images` 中，**不在 `toolResults` 中**。

### kiro-cli 的 fs_read Image 模式

```json
{
  "name": "fs_read",
  "input": {
    "operations": [{ "mode": "Image", "image_paths": ["C:\\path\\to\\image.png"] }]
  }
}
```

---

## 11. 响应流事件格式

响应是二进制 AWS event stream，JSON 事件嵌入其中。

### 事件类型

| 事件 | 格式 | 说明 |
|------|------|------|
| 初始响应 | `{"conversationId":""}` | 会话确认 |
| 文本内容 | `{"content":"文本片段"}` | 流式文本输出 |
| Tool use 开始 | `{"name":"fs_read","toolUseId":"tooluse_xxx"}` | 无 `input`/`stop` |
| Tool input 增量 | `{"input":"参数片段","name":"fs_read","toolUseId":"tooluse_xxx"}` | **有 name 字段** |
| Tool use 结束 | `{"name":"fs_read","stop":true,"toolUseId":"tooluse_xxx"}` | |
| Followup 提示 | `{"followupPrompt":"..."}` | 忽略 |
| 上下文使用率 | `{"contextUsagePercentage":0.226}` | |

### 事件解析规则

```
初始事件：有 name + toolUseId，无 input，无 stop
Input 事件：有 input 字段（⚠️ 也有 name 和 toolUseId）
Stop 事件：有 stop: true
Content 事件：有 content 字段，无 followupPrompt
```

**关键陷阱**：`input` 事件中**包含 `name` 字段**，不能用 `!hasName` 来区分初始事件和 input 事件。正确方式：
- 初始事件 = 有 `name` + 无 `input` + 无 `stop`
- Input 事件 = 有 `input`

### Tool arguments 重组

`input` 事件是增量的，需要拼接所有 `input` 值，在 `stop` 事件时解析为 JSON：

```
{"input":"",  ...}     → ""
{"input":"{\"path\":", ...} → "{\"path\":"
{"input":"\"C:\\\\tmp\"", ...} → "{\"path\":\"C:\\\\tmp\""
{"input":"}", ...}     → "{\"path\":\"C:\\\\tmp\"}"
{"stop":true, ...}     → 解析 "{\"path\":\"C:\\\\tmp\"}" 为 JSON
```

---

## 12. client-telemetry（Toolkit 遥测）

独立于 Q API 的遥测通道，需要 AWS SigV4 签名。

### 请求

```http
POST https://client-telemetry.us-east-1.amazonaws.com/metrics
content-type: application/json
user-agent: aws-sdk-rust/1.3.14 os/{os} lang/rust/1.92.0
x-amz-user-agent: aws-sdk-rust/1.3.14 ua/2.1 api/toolkittelemetry/1.0.0 os/{os} lang/rust/1.92.0 exec-env/AmazonQ-For-CLI Version/2.0.1 app/AmazonQ-For-CLI
x-amz-date: 20260421T013114Z
authorization: AWS4-HMAC-SHA256 Credential={AccessKeyId}/{date}/{region}/execute-api/aws4_request, SignedHeaders=content-length;content-type;host;x-amz-date;x-amz-security-token;x-amz-user-agent, Signature={sig}
x-amz-security-token: {STS 临时会话令牌}
amz-sdk-request: attempt=1; max=1
amz-sdk-invocation-id: {uuid}
accept: */*
accept-encoding: gzip
host: client-telemetry.us-east-1.amazonaws.com
```

> 注意：
> - `content-type` 是 `application/json`（不是 `x-amz-json-1.0`）
> - `user-agent` 比 Q API 简短，没有 `exec-env` 和 `md/appVersion`
> - `amz-sdk-request` 是 `max=1`（不重试），Q API 是 `max=3`
> - 认证使用 **AWS SigV4 签名**，需要 STS 临时凭证（通过 `GetCredentialsForIdentity` 获取）
> - **无法用 Bearer Token 调用**

### 请求 Body

```json
{
  "AWSProduct": "CodeWhisperer for Terminal",
  "AWSProductVersion": "2.0.1",
  "ClientID": "{固定 uuid}",
  "OS": "windows",
  "OSArchitecture": "x86_64",
  "OSVersion": "Windows 10 Pro (or newer) - build 19045",
  "MetricData": [{
    "MetricName": "codewhispererterminal_addChatMessage",
    "EpochTimestamp": 1776735078817,
    "Unit": "None",
    "Value": 1.0,
    "Metadata": [
      { "Key": "amazonqConversationId", "Value": "{conversationId}" },
      { "Key": "codewhispererterminal_model", "Value": "auto" },
      { "Key": "codewhispererterminal_timeToFirstChunksMs", "Value": "3173.458" },
      { "Key": "codewhispererterminal_toolName", "Value": "fs_read" },
      { "Key": "result", "Value": "Succeeded" }
    ]
  }]
}
```

### Metric 类型（2.2.2 实抓）

一次完整 chat（无工具调用）期间 kiro-cli 2.2.2 实测发送以下 metrics（按时间顺序）：

| 顺序 | MetricName | 触发时机 |
|------|-----------|----------|
| 1 | `codewhispererterminal_cliSubcommandExecuted` | kiro-cli 进程启动 |
| 2 | `codewhispererterminal_agentConfigInit` | 加载 agent 配置完成 |
| 3 | `codewhispererterminal_addChatMessage` | 每轮 assistant 消息完成（另一份同名事件也通过 Q API `SendTelemetryEvent` 发送）|
| 4 | `codewhispererterminal_recordUserTurnCompletion` | 一轮 user turn 全部完成 |

其它已知 MetricName（本次未触发）：
| MetricName | 触发时机 |
|------------|----------|
| `codewhispererterminal_toolUseSuggested` | 模型建议使用工具 |

> 原 doc 中的「KiroChatClient 不实现此通道」在 2.2.2 中**已失效**：实测 2.2.2 依然并行使用 Q API `SendTelemetryEvent`（Bearer Token）+ `client-telemetry/metrics`（SigV4）两个通道。

### Metadata 字段集（实测）

**1. `cliSubcommandExecuted`**

| Key | 示例 |
|-----|------|
| `credentialStartUrl` | `""`（social 登录时为空；SSO 时为 Start URL）|
| `codewhispererterminal_subcommand` | `chat` |
| `codewhispererterminal_inCloudshell` | `""` |
| `codewhispererterminal_clientApplication` | `""` |

**2. `agentConfigInit`**

| Key | 示例 |
|-----|------|
| `credentialStartUrl` | `""` |
| `amazonqConversationId` | `{uuid}` |
| `codewhispererterminal_agentsLoadedCount` | `0` |
| `codewhispererterminal_agentsFailedToLoadCount` | `0` |
| `codewhispererterminal_legacyProfileMigrationExecuted` | `false` |
| `codewhispererterminal_legacyProfileMigratedCount` | `0` |
| `codewhispererterminal_launchedAgent` | `kiro_default` |

**3. `addChatMessage`**

| Key | 示例 |
|-----|------|
| `amazonqConversationId` | `{uuid}` |
| `codewhispererterminal_utteranceId` | `{messageId uuid}`（与 `SendTelemetryEvent.messageId` 一致）|
| `credentialStartUrl` / `ssoRegion` | `""`（social 时为空） |
| `codewhispererterminal_inCloudshell` | `""` |
| `codewhispererterminal_contextFileLength` | 上下文文本长度（chars） |
| `requestId` | Q API 返回的 x-amzn-RequestId |
| `result` / `reason` / `reasonDesc` / `statusCode` | `Succeeded` / 空 / 空 / 空 |
| `codewhispererterminal_model` | `auto` |
| `codewhispererterminal_timeToFirstChunksMs` | 如 `2286.470` |
| `codewhispererterminal_timeBetweenChunksMs` | 如 `"0.840,0.007,0.008,0.003"`（逗号分隔字符串）|
| `codewhispererterminal_chatConversationType` | `NotToolUse` / `ToolUse` |
| `codewhispererterminal_toolUseId` / `codewhispererterminal_toolName` | 有工具调用时填 |
| `codewhispererterminal_assistantResponseLength` | `2` |
| `codewhispererterminal_chatMessageMetaTags` | `""` |
| `codewhispererterminal_clientApplication` | `""` |

**4. `recordUserTurnCompletion`**

| Key | 示例 |
|-----|------|
| `amazonqConversationId` | `{uuid}` |
| `credentialStartUrl` / `ssoRegion` | `""`（social 时为空） |
| `codewhispererterminal_inCloudshell` | `""` |
| `requestId` | 同 `addChatMessage.requestId` |
| `codewhispererterminal_utteranceId` | 同 `addChatMessage.utteranceId` |
| `result` / `reason` / `reasonDesc` / `statusCode` | `Succeeded` / 空 / 空 / 空 |
| `codewhispererterminal_chatConversationType` | `NotToolUse` |
| `codewhispererterminal_timeToFirstChunksMs` | `2286.470` |
| `codewhispererterminal_userPromptLength` | `188` |
| `codewhispererterminal_assistantResponseLength` | `2` |
| `codewhispererterminal_userTurnDurationSeconds` | `2` |
| `codewhispererterminal_followUpCount` | `0` |
| `codewhispererterminal_chatMessageMetaTags` | `""` |
| `codewhispererterminal_isSubagent` | `false` |
| `codewhispererterminal_parentToolUseId` | `""` |

**注意**：此通道需要 STS 临时凭证（通过 `GetCredentialsForIdentity` 获取，凭证缓存在 sqlite `state.telemetry-cognito-credentials`），无法用 Bearer Token 调用。

2026-05-09 发布前复测：连续发送 Toolkit telemetry 时，若每个新进程都重新调用 Cognito
`GetId` / `GetCredentialsForIdentity`，可能触发 `TooManyRequestsException: Rate exceeded`。kiro-cli 本地
`state.telemetry-cognito-identity-id` 和 `state.telemetry-cognito-credentials` 可复用；调用端应优先读取未过期缓存，
只在缺失或过期时再请求 Cognito。

---

## 13. 启动时版本检查

kiro-cli 进程启动时首先发一次无认证的 HTTP GET 拉取最新版本清单：

### 请求

```http
GET https://desktop-release.q.us-east-1.amazonaws.com/latest/manifest.json
accept: */*
accept-encoding: gzip
```

> 无 `authorization`、无 UA（或仅默认 rust reqwest UA）、无 content-type。

### 响应

```jsonc
{
  "version": "2.2.2",
  "packages": [
    {
      "kind": "deb",
      "targetTriple": "x86_64-unknown-linux-gnu",
      "os": "linux",
      "fileType": "deb",            // deb / tarXz / tarGz / tarZst / zip / appImage / dmg / exe
      "architecture": "x86_64",     // x86_64 / aarch64 / universal
      "variant": "full",            // full / headless
      "download": "2.2.2/kiro-cli.deb",
      "sha256": "d0ba204ed55cb53edb0b46bafb36107750d8362ded002d5db0505fcd51f000cd",
      "size": 390382758,
      "channel": "stable"
    }
    // ... 其他平台包
  ]
}
```

| 字段 | 说明 |
|------|------|
| `version` | 最新版本号 |
| `packages[]` | 各平台 / 架构 / 格式的安装包列表 |
| `packages[].download` | 相对路径，完整 URL 为 `https://desktop-release.q.{region}.amazonaws.com/{download}` |
| `packages[].sha256` | 下载后校验 |
| `packages[].channel` | 目前观察到 `stable` 一种 |

CLI 比对 `version` 和本地版本，若不同则在下次运行时提示升级。此请求是 **lifecycle 第一次网络活动**（在 OIDC/Social refresh 之前），**走 `HTTPS_PROXY`**。
