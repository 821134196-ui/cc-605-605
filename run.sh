#!/usr/bin/env bash
# 一条命令启动：编译并运行门店折扣码系统（Go + Gin + GORM + SQLite，前端静态资源已嵌入二进制）。
set -euo pipefail

cd "$(dirname "$0")/backend"

# 允许使用本机 go；找不到时回退到 ~/tools/go。
if ! command -v go >/dev/null 2>&1; then
  if [ -x "$HOME/tools/go/bin/go" ]; then
    export PATH="$HOME/tools/go/bin:$PATH"
  else
    echo "未找到 Go 工具链（>=1.23），请先安装: https://go.dev/dl/" >&2
    exit 1
  fi
fi

ADDR="${ADDR:-:8080}"
DB="${DB:-promoleak.db}"

if [ "${1:-}" = "--reset" ]; then
  rm -f "$DB" "$DB-wal" "$DB-shm"
  echo "已重置数据库 $DB"
  shift || true
fi

echo ">> 编译中（首次需要拉取依赖）…"
go build -o promoleak .

echo ">> 启动服务 http://localhost${ADDR}（Ctrl-C 停止）"
exec ./promoleak -addr "$ADDR" -db "$DB"
