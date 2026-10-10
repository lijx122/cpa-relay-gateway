# CPA-Relay-Gateway (PoC 原型 / 半成品归档)

> **⚠️ 状态说明：实验性半成品（Proof of Concept）**  
> 本项目为探索将 **NewAPI 跨协议转码能力 (RelayKit)** 与 **CLIProxyAPI (CPA) 原生凭据池** 深度融合、剔除双重网关套壳与本地 HTTP 回环的轻量化无头网关原型。  
> **目前存在较多未完备的边缘治理与生态问题，暂不作为主力生产环境使用。主力生产环境继续保持成熟的 `NewAPI + CPA` 双层拓扑，本项目就此归档封存，供后续参考与迭代。**

---

## 🎯 探索初衷与架构设计

在原有架构中，个人工作台采用两层代理架构：
- **第一层 (NewAPI)**：负责统一路由、计费、密钥管理，但对反代渠道只能通过配置 `Type 58 (AdvancedCustom)` 本地 HTTP 回环转发到 `127.0.0.1:8317`；
- **第二层 (CLIProxyAPI)**：负责管理本地 OAuth 凭据 (`auths/*.json`)、账号轮询调度与官方协议封装。

**本原型的目标**：
利用一个仅 **~9MB** 的静态 Go 单二进制程序，直接读取本地凭据池，直接在内存中基于 RelayKit 实施 4-way 双向跨协议转码（OpenAI Chat ↔ Responses ↔ Claude Messages ↔ Gemini），消除本地进程间网络跳跃。

```text
客户端 (OpenAI/Claude/Gemini/Responses)
               │
               ▼
┌──────────────────────────────────────────────┐
│           cpa-relay-gateway (单进程)          │
│                                              │
│  [HTTP Router]                               │
│       │                                      │
│       ├──► 商业 API 渠道 (DeepSeek, Groq...)  │
│       │                                      │
│       └──► 原生凭据池 (xAI, Codex, Antigravity)│
│                 │ (直接读取 auths/*.json)     │
│                 ▼                            │
│           官方端点直连 (无外部代理依赖)          │
└──────────────────────────────────────────────┘
```

---

## ✅ 已验证能力与完成特性

1. **RelayKit 跨协议转码**：基于 `github.com/QuantumNous/new-api/relaykit` v0.3.0，支持客户端以任意协议打入并向上游任意协议派发（单次请求与 SSE 流式均已跑通）。
2. **原生文件凭据池 (`pkg/authpool`)**：无需启动 CPA 进程，直接扫描读取 `auths/*.json`，实现无锁原子轮询调度与官方指纹/头注入（如 xAI 1.0.13 指纹、Google Antigravity Hub UA）。
3. **极低资源开销**：单静态二进制（~9.2MB），常驻内存占用仅 ~10.2MB，单次请求内存耗时 < 1ms。
4. **零停机动态热重载**：基于 `fsnotify` + `sync/atomic.Pointer`，修改 `config.yaml` 或新增凭据无需重启，平滑热切。
5. **思考流初版注入**：支持 Antigravity 的 `includeThoughts` 和思考预算约束初步修补。

---

## 🚧 已知缺陷与遗留问题（归档原因）

1. **复杂的模型思考（Thinking / Reasoning）自适应体系不完善**：
   - 官方 CPA 内部积累了上千行复杂的思维链提取、签名暂存 (`thoughtSignature`)、多轮缓存回填与客户端自适应降级逻辑。本网关目前仅做了初级补丁，多轮复杂工具调用与长上下文下的 Thinking 流仍不稳定。
2. **缺乏完善的多级冷却与异常容错机制**：
   - CPA 具备针对 429 Short Cooldown、Daily Quota Exhausted、403 Unsupported Client 的精密状态机降级切号逻辑；本原型仅有基础原子轮询。
3. **Token 与租户鉴权管理缺失**：
   - 目前采用宽松放行机制，尚未集成类似 NewAPI 的精细 Token 分配、模型白名单限制与配额拦截体系。
4. **维护与迁移机会成本**：
   - 现役 `NewAPI + CPA` 虽有轻微套壳开销，但在功能完备性、界面看板和成熟度上经过了长期验证。在考公备考等高优先级主线任务下，继续深挖小众网关边际效益递减。

---

## 📦 项目结构

```text
cpa-relay-gateway/
├── cmd/gateway/           # 入口 main.go
├── pkg/
│   ├── config/            # YAML 配置解析与动态文件监听热重载
│   ├── relay/             # 基于 RelayKit 的多协议转换与流式 SSE 管道
│   ├── authpool/          # 原生读取本地 auths/*.json 凭据调度器
│   └── server/            # HTTP 路由与分发转发核心
├── config.example.yaml    # 脱敏配置模板
├── migrate_configs.py     # 从 NewAPI SQLite + CPA 自动生成配置的脚本
└── README.md
```

---

## 📄 License
MIT License.
