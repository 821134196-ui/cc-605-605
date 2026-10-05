#!/usr/bin/env bash
# 一条命令启动：构建前端（嵌入 Go 二进制）→ 编译 → 启动服务（SQLite 本地文件）。
#   ./run.sh            生产形态：单个 Go 进程同时提供 API 与前端，默认 :8080
#   ./run.sh dev        开发形态：Vite 热更新(5173) + Go API(8080)
#   ./run.sh test       运行后端全部测试 + 端到端 curl 验证
set -euo pipefail
cd "$(dirname "$0")"

# --- 工具链探测：优先 PATH，其次用户目录安装的 Go ---
if ! command -v go >/dev/null 2>&1; then
  [ -x "$HOME/.local/go/bin/go" ] && export PATH="$HOME/.local/go/bin:$PATH"
fi
command -v go >/dev/null 2>&1 || { echo "未找到 go（已检查 PATH 与 ~/.local/go）"; exit 1; }
export CGO_ENABLED=0
export GOPATH="${GOPATH:-$HOME/go}"
export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"

ADDR="${DG_ADDR:-:8080}"
DB="${DG_DB:-data/app.db}"
mkdir -p data bin

build_frontend() {
  if [ -f cmd/server/web/index.html ] && [ "${REBUILD_FE:-0}" != "1" ]; then
    echo "→ 前端产物已存在（设置 REBUILD_FE=1 可强制重建）"
    return
  fi
  echo "→ 构建前端（Vue3 + Vite）"
  ( cd frontend
    [ -d node_modules ] || npm install --no-audit --no-fund
    npm run build )
}

run_tests() {
  echo "→ Go 单元/并发/幂等测试（-race 需要 gcc，自动回退普通模式）"
  if CGO_ENABLED=1 go test -race -count=1 ./... 2>/dev/null; then :; else
    echo "  （race 模式不可用，使用普通模式）"; CGO_ENABLED=0 go test -count=1 ./...
  fi
  echo "→ 端到端验证（真实 HTTP + SQLite）"
  CGO_ENABLED=0 go build -o bin/server ./cmd/server
  rm -f data/e2e.db
  DG_DB=data/e2e.db DG_ADDR=:18099 ./bin/server >/tmp/dg-e2e.log 2>&1 &
  SRV=$!
  trap 'kill $SRV 2>/dev/null || true' EXIT
  sleep 1.5
  bash scripts/e2e.sh 18099
  kill $SRV 2>/dev/null || true
  rm -f data/e2e.db
  echo "✔ 全部验证通过"
}

case "${1:-serve}" in
  test)
    run_tests
    ;;
  dev)
    command -v npm >/dev/null 2>&1 || { echo "dev 模式需要 node/npm"; exit 1; }
    echo "→ 启动 API $ADDR（前端走 Vite :5173 代理 /api）"
    CGO_ENABLED=0 go run ./cmd/server &
    API=$!
    ( cd frontend && [ -d node_modules ] || npm install --no-audit --no-fund; npm run dev ) &
    WEB=$!
    trap 'kill $API $WEB 2>/dev/null || true' INT TERM EXIT
    wait
    ;;
  serve|"")
    build_frontend
    echo "→ 编译并启动服务 http://localhost$ADDR （数据库 $DB）"
    CGO_ENABLED=0 go build -o bin/server ./cmd/server
    exec env DG_ADDR="$ADDR" DG_DB="$DB" ./bin/server
    ;;
  *)
    echo "用法: $0 [serve|dev|test]"; exit 1
    ;;
esac
