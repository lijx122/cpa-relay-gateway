import sqlite3
import json
import yaml

# 1. 读取 NewAPI 数据库
conn = sqlite3.connect(r'D:\ai-api\newapi\one-api.db')
cursor = conn.cursor()

cursor.execute("SELECT id, name, type, status, base_url, models, model_mapping, key FROM channels WHERE status = 1 OR status = 2;")
rows = cursor.fetchall()

# NewAPI 类型映射到自研网关协议
# Type: 1 = OpenAI, 14 = Anthropic, 24 = Gemini, 25 = Moonshot/Kimi (OpenAI 兼容), 43 = DeepSeek (OpenAI 兼容), 58 = 自定义/中转 (OpenAI 兼容)
def map_newapi_type(type_id, base_url):
    if type_id == 24:
        return "gemini", "https://generativelanguage.googleapis.com"
    elif type_id == 14:
        return "claude_messages", base_url or "https://api.anthropic.com"
    elif type_id == 20: # OpenRouter
        return "openai_chat", base_url or "https://openrouter.ai/api/v1"
    else:
        # 默认 openai 兼容
        b = base_url.strip() if base_url else "https://api.openai.com/v1"
        return "openai_chat", b

channels = []
model_mappings = {}

for r in rows:
    ch_id, name, type_id, status, base_url, models_str, mapping_str, key = r
    
    # 忽略 NewAPI 内部配的指向本地 8317 CPA 的两个渠道，因为我们自研网关直接通过 auth_pools 接管 CPA 凭据！
    if "8317" in (base_url or ""):
        print(f"Skipping local CPA loopback channel in NewAPI: {name}")
        continue
    
    protocol, resolved_base = map_newapi_type(type_id, base_url or "")
    models_list = [m.strip() for m in models_str.split(",") if m.strip()] if models_str else []
    
    ch_cfg = {
        "name": f"newapi-{name}",
        "base_url": resolved_base,
        "api_key": key,
        "protocol": protocol,
        "models": models_list
    }
    channels.append(ch_cfg)
    
    # 解析 mapping
    if mapping_str and mapping_str.strip():
        try:
            m_dict = json.loads(mapping_str)
            for src_m, dst_m in m_dict.items():
                model_mappings[src_m] = {
                    "target_model": dst_m,
                    "channel": f"newapi-{name}"
                }
        except Exception as e:
            print(f"Failed to parse mapping for {name}: {e}")

# 2. 加入 CPA 原生凭据池 (xai, codex, antigravity)
auth_pools = [
    {
        "name": "cpa-grok-pool",
        "type": "xai",
        "auth_dir": r"D:\ai-api\cliproxy\auths",
        "models": [
            "grok-3-mini",
            "grok-3-mini-fast",
            "grok-4.3",
            "grok-4.5",
            "grok-4.6",
            "grok-4.7",
            "grok-4.7-build-fast",
            "grok-4.20-0309-reasoning",
            "grok-4.20-0309-non-reasoning",
            "grok-4.20-multi-agent-0309",
            "grok-imagine-image",
            "grok-imagine-image-quality",
            "grok-imagine-image-2.0",
            "grok-imagine-video",
            "grok-imagine-video-1.5",
            "grok-imagine-video-1.5-preview",
            "grok-build-0.1",
            "grok-composer-2.5-fast"
        ]
    },
    {
        "name": "cpa-codex-pool",
        "type": "codex",
        "auth_dir": r"D:\ai-api\cliproxy\auths",
        "models": [
            "gpt-5.5",
            "gpt-5.6-luna",
            "gpt-5.6-terra",
            "gpt-6-luna",
            "gpt-oss-120b-medium",
            "codex-auto-review"
        ]
    },
    {
        "name": "cpa-antigravity-pool",
        "type": "antigravity",
        "auth_dir": r"D:\ai-api\cliproxy\auths",
        "models": [
            "gemini-3.8-flash",
            "gemini-3.8-flash-high",
            "gemini-3.7-flash-high",
            "gemini-3.6-flash-high",
            "gemini-3.5-flash-lite",
            "gemini-3.1-pro-low",
            "gemini-3.1-flash-lite",
            "gemini-3.1-flash-image",
            "gemini-3-flash-preview",
            "gemini-pro-agent",
            "claude-sonnet-4-6",
            "claude-opus-4-6",
            "claude-opus-4-6-thinking",
            "claude-sonnet-5-5-high",
            "claude-opus-5-5-high"
        ]
    }
]

# 注入常用的便捷别名映射
extra_mappings = {
    "claude-sonnet": {
        "target_model": "claude-sonnet-4-6",
        "auth_pool": "cpa-antigravity-pool"
    },
    "claude-opus": {
        "target_model": "claude-opus-4-6",
        "auth_pool": "cpa-antigravity-pool"
    },
    "gemini-flash": {
        "target_model": "gemini-3.8-flash",
        "auth_pool": "cpa-antigravity-pool"
    },
    "codex-latest": {
        "target_model": "gpt-5.5",
        "auth_pool": "cpa-codex-pool"
    },
    "grok-mini": {
        "target_model": "grok-3-mini",
        "auth_pool": "cpa-grok-pool"
    },
    "deepseek": {
        "target_model": "deepseek-flash",
        "channel": "newapi-ds"
    }
}
for k, v in extra_mappings.items():
    model_mappings[k] = v

config_data = {
    "server": {
        "port": 8787,
        "host": "127.0.0.1"
    },
    "channels": channels,
    "auth_pools": auth_pools,
    "model_mappings": model_mappings
}

out_path = r"F:\cx\cpa-relay-gateway\config.production.yaml"
with open(out_path, "w", encoding="utf-8") as f:
    yaml.dump(config_data, f, allow_unicode=True, sort_keys=False)

print(f"\nMigration successful! Config written to: {out_path}")
print(f"Total NewAPI migrated channels: {len(channels)}")
print(f"Total CPA auth pools: {len(auth_pools)}")
print(f"Total model mappings: {len(model_mappings)}")
