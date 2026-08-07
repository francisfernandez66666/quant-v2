#!/bin/bash
# 只读探活脚本：测 SiliconFlow GLM-Z1 连通性（含流式 SSE）
set -u
ADDR="${LLM_API_URL:-https://api.siliconflow.cn}"
KEY="${LLM_API_KEY:-}"
MODEL="${LLM_MODEL:-THUDM/GLM-Z1-9B-0414}"
if [ -z "$KEY" ]; then echo "未设置 LLM_API_KEY 环境变量"; exit 1; fi
URL="${ADDR}/v1/chat/completions"
echo "== 目标: $URL model=$MODEL =="
echo "== 1) 非流式 最小请求 =="
curl -sS -m 60 -w "\n[HTTP=%{http_code} 耗时=%{time_total}s]\n" \
  -X POST "$URL" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"回答:好\"}],\"stream\":false,\"max_tokens\":8}" 2>&1
echo "== 2) 流式 (stream:true) =="
curl -sS -m 25 -w "\n[HTTP=%{http_code} 耗时=%{time_total}s]\n" \
  -X POST "$URL" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"回答:好\"}],\"stream\":true,\"max_tokens\":8}" 2>&1
