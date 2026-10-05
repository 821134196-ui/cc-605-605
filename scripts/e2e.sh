#!/usr/bin/env bash
# 端到端验证：启动真实服务（Gin + SQLite），用 curl 走通
#   1) 封禁前核价边界  2) 封禁后待付款重新核价/已付款保留快照
#   3) 同码并发核销只成功一次  4) 支付 vs 封禁竞争、订单只有一个折扣金额
#   5) 重复支付回执幂等  6) 误封恢复限制  7) 影响清单追溯到原优惠
set -uo pipefail

PORT="${1:-18080}"
BASE="http://localhost:$PORT/api"
PASS=0; FAIL=0
ok()   { if [ "$1" = "$2" ]; then PASS=$((PASS+1)); echo "  ✓ $3"; else FAIL=$((FAIL+1)); echo "  ✗ $3 (want=$1 got=$2)"; fi; }
have() { if [ -n "$1" ]; then PASS=$((PASS+1)); echo "  ✓ $2"; else FAIL=$((FAIL+1)); echo "  ✗ $2 (空值)"; fi; }

j()  { command -v jq >/dev/null && jq -r "$2" <<<"$1"; }
jp() { python3 -c "import json,sys;d=json.load(sys.stdin);print(eval(sys.argv[1]))" "$1" <<<"$2" 2>/dev/null; }

echo "== 0. 读取种子数据 =="
PRODUCTS=$(curl -s "$BASE/products")
CODES=$(curl -s "$BASE/codes")
# 列表拿不到完整码（只显示尾号）——用种子里的已知码
FULL="NC-2026-8K2Q-47FD"
TAIL=$(curl -s "$BASE/codes" | python3 -c "import json,sys;print([c['tail'] for c in json.load(sys.stdin) if c['id']==1][0])")
ok "****47FD" "$TAIL" "码列表只显示尾号"
LEAK=$(python3 -c "import json,sys; s=json.dumps(json.load(sys.stdin)); print('LEAK' if 'NC-2026-8K2Q-47FD' in s else 'safe')" <<<"$CODES")
ok "safe" "$LEAK" "列表响应不含完整码"

echo "== 1. 封禁前核价边界 =="
Q=$(curl -s -X POST "$BASE/quote" -H 'Content-Type: application/json' \
  -d '{"member_level":"normal","items":[{"product_id":1,"qty":1}],"code":"'"$FULL"'"}')
D=$(python3 -c "import json,sys;print(json.load(sys.stdin)['discount_amount'])" <<<"$Q")
F=$(python3 -c "import json,sys;print(json.load(sys.stdin)['final_amount'])" <<<"$Q")
ok 600 "$D" "30元8折券折扣=6元"
ok 2400 "$F" "折后应付=24元"

# 会员等级不符（gold 专属批次码用在 normal）
QG=$(curl -s -X POST "$BASE/quote" -H 'Content-Type: application/json' \
  -d '{"member_level":"normal","items":[{"product_id":1,"qty":1}],"code":"GB-2026-GLD7-15OFF"}')
ok "MEMBER_NOT_ALLOWED" "$(python3 -c "import json,sys;print(json.load(sys.stdin)['reason'])" <<<"$QG")" "非目标会员被拒"

# 过期码
QE=$(curl -s -X POST "$BASE/quote" -H 'Content-Type: application/json' \
  -d '{"member_level":"normal","items":[{"product_id":1,"qty":1}],"code":"ND-2026-OLD1-EXPIRED"}')
ok "EXPIRED" "$(python3 -c "import json,sys;print(json.load(sys.stdin)['reason'])" <<<"$QE")" "过期码被拒"

echo "== 2. 下单：一笔待付款 A，一笔先付款 B =="
OA=$(curl -s -X POST "$BASE/orders" -H 'Content-Type: application/json' \
  -d '{"member_level":"normal","items":[{"product_id":1,"qty":1}],"code":"'"$FULL"'"}')
AID=$(python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" <<<"$OA")
# 第二张码给 B
OB=$(curl -s -X POST "$BASE/orders" -H 'Content-Type: application/json' \
  -d '{"member_level":"normal","items":[{"product_id":1,"qty":1}],"code":"NC-2026-9W1T-82MA"}')
BID=$(python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" <<<"$OB")
PB=$(curl -s -X POST "$BASE/orders/$BID/pay" -H 'Content-Type: application/json' -d '{"receipt_no":"E2E-RCP-B"}')
ok 2400 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['payment']['paid_amount'])" <<<"$PB")" "订单B按折后24元支付"

echo "== 3. 封禁批次1 =="
BAN=$(curl -s -X POST "$BASE/batches/1/ban" -H 'Content-Type: application/json' -d '{"reason":"渠道泄露"}')
ok 3 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['banned_codes'])" <<<"$BAN")" "批次下3个码被封"
ok 1 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['requoted_orders'])" <<<"$BAN")" "1笔待付款被重新核价"
ok 1 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['affected_paid'])" <<<"$BAN")" "1笔已付款进入影响清单"

GA=$(curl -s "$BASE/orders/$AID")
ok 3000 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['order']['final_amount'])" <<<"$GA")" "待付款A重新核价为原价30元"
ok 2 "$(python3 -c "import json,sys;print(len(json.load(sys.stdin)['price_history']))" <<<"$GA")" "A留下两条核价历史"
HREASON=$(python3 -c "import json,sys;print(json.load(sys.stdin)['price_history'][1]['reason'])" <<<"$GA")
ok "BAN_STRIPPED" "$HREASON" "第二条历史标记 BAN_STRIPPED"

GB=$(curl -s "$BASE/orders/$BID")
ok 2400 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['order']['final_amount'])" <<<"$GB")" "已付款B金额快照保持24元"
ok 600 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['order']['applied_rule']['discount_amount'])" <<<"$GB")" "B订单详情保留优惠规则快照"

echo "== 4. A 封禁后支付：按原价，码不核销 =="
PA=$(curl -s -X POST "$BASE/orders/$AID/pay" -H 'Content-Type: application/json' -d '{"receipt_no":"E2E-RCP-A"}')
ok 3000 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['payment']['paid_amount'])" <<<"$PA")" "A实付30元"
CODEID_A=$(python3 -c "import json,sys;print(json.load(sys.stdin)['payment']['code_id'])" <<<"$PA")
ok "None" "$CODEID_A" "A的回执未核销任何码"

echo "== 5. 影响清单只含 B，且能追到原优惠 =="
IMP=$(curl -s "$BASE/impact?batch_id=1")
N=$(python3 -c "import json,sys;print(len(json.load(sys.stdin)))" <<<"$IMP")
ok 1 "$N" "影响清单仅1笔（真实使用优惠的已付款订单）"
IMPBID=$(python3 -c "import json,sys;print(json.load(sys.stdin)[0]['order']['id'])" <<<"$IMP")
ok "$BID" "$IMPBID" "影响清单中的订单是B"
IMPDISC=$(python3 -c "import json,sys;print(json.load(sys.stdin)[0]['payment']['applied_rule']['discount_amount'])" <<<"$IMP")
ok 600 "$IMPDISC" "影响清单追到当时优惠6元"
IMPTAIL=$(python3 -c "import json,sys;print(json.load(sys.stdin)[0]['payment']['applied_rule']['code_tail'])" <<<"$IMP")
ok "82MA" "$IMPTAIL" "影响清单保留码尾号82MA"

echo "== 6. 重复支付回执幂等 =="
DUP=$(curl -s -X POST "$BASE/orders/$BID/pay" -H 'Content-Type: application/json' -d '{"receipt_no":"E2E-RCP-B"}')
ok "True" "$(python3 -c "import json,sys;print(json.load(sys.stdin)['idempotent_replay'])" <<<"$DUP")" "同一回执重复到达=幂等重放"
RCPCNT=$(curl -s "$BASE/payments" | python3 -c "import json,sys;print(sum(1 for p in json.load(sys.stdin) if p['receipt_no']=='E2E-RCP-B'))")
ok 1 "$RCPCNT" "同一回执只落一条记录"

echo "== 7. 同码并发核销：新建N单用同一张码，同时支付 =="
# 用第三张码（随批次封禁已失效，需先误封恢复不可行——改用新批次新码）
# 批次2/码4 当前是 active 状态（gold 券未封）；但会员限定 gold。
# 用码4(gold券)+gold会员 建12单
IDS=()
for i in $(seq 1 12); do
  O=$(curl -s -X POST "$BASE/orders" -H 'Content-Type: application/json' \
    -d '{"member_level":"gold","items":[{"product_id":3,"qty":1}],"code":"GB-2026-GLD7-15OFF"}')
  IDS+=("$(python3 -c "import json,sys;print(json.load(sys.stdin)['id'])" <<<"$O")")
done
PAYLOG=$(mktemp)
for id in "${IDS[@]}"; do
  (curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/orders/$id/pay" \
    -H 'Content-Type: application/json' -d '{"receipt_no":"CONC-'"$id"'"}') >>"$PAYLOG" &
done
wait
C200=$(grep -c 200 "$PAYLOG" || true)
C409=$(grep -c 409 "$PAYLOG" || true)
ok 1 "$C200" "12 并发支付仅 1 笔成功（200）"
ok 11 "$C409" "其余 11 笔收到 409（码已核销）"
REDEEMED=$(curl -s "$BASE/codes?batch_id=2" | python3 -c "import json,sys;print(json.load(sys.stdin)[0]['state'])")
ok "redeemed" "$REDEEMED" "码最终状态=redeemed（批次2未封禁）"

echo "== 8. 误封恢复限制 =="
# 已核销码（码4）尝试单码恢复
RC4=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/codes/4/restore")
ok 409 "$RC4" "已核销码恢复被拒(409)"
# 过期批次码（码5）：先单码封再恢复
curl -s -o /dev/null -X POST "$BASE/codes/5/ban" -H 'Content-Type: application/json' -d '{"reason":"t"}'
RC5=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/codes/5/restore")
ok 409 "$RC5" "过期码恢复被拒(409)"
# 有效且未使用：批次1的码当前随批次被封，先解封批次；批次封禁不连坐码
curl -s -o /dev/null -X POST "$BASE/batches/1/restore" -H 'Content-Type: application/json'
# 码3没有独立 code ban（批次封不连坐码），它现在应直接可用
Q3=$(curl -s -X POST "$BASE/quote" -H 'Content-Type: application/json' \
  -d '{"member_level":"normal","items":[{"product_id":1,"qty":1}],"code":"NC-2026-LP4R-06XZ"}')
ok 600 "$(python3 -c "import json,sys;print(json.load(sys.stdin)['discount_amount'])" <<<"$Q3")" "批次解封后未使用码恢复可用"
# 已核销的码2不能随批次解封重开
ST2=$(curl -s "$BASE/codes?batch_id=1" | python3 -c "import json,sys;print([c for c in json.load(sys.stdin) if c['id']==2][0]['state'])")
ok "redeemed" "$ST2" "已核销码2在批次解封后仍为 redeemed"

echo
echo "结果: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
