# 门店折扣码泄露封禁与订单追溯系统

连锁门店折扣码外泄后的应急系统：**立即停用受影响码 → 待付款单重新核价 → 已付款单保留原优惠并入影响清单逐笔追溯**。

## 一条命令启动

```bash
./run.sh              # 编译并启动，访问 http://localhost:8080
./run.sh --reset      # 重置演示数据库后启动
ADDR=:9090 ./run.sh   # 自定义端口
```

> 需要 Go ≥ 1.23 与 gcc（SQLite 驱动为 cgo）。前端 Vue 3 运行时已本地化在
> `backend/web/vendor/`，无需 npm 构建，静态资源通过 `go:embed` 打进单一二进制。

## 业务规则

| 场景 | 规则 |
|---|---|
| 折扣码批次 | 按批次设置适用会员等级、适用商品、有效期、优惠规则（百分比 / 立减） |
| 封禁后待付款单 | 立即重新核价：解除优惠、`discount_fen` 归 0、应付恢复商品原价 |
| 封禁后已付款单 | **保留当时确认的优惠规则与金额快照**，进入「泄露影响清单」供追溯 |
| 普通码列表 | 只显示尾 4 位（`****************LQ17`），明文码仅在结账输入时出现 |
| 订单详情 | 保留采用的优惠规则（批次、规则、尾号）与实际优惠金额、核销时间快照 |
| 支付/封禁同时发生 | 以**服务端核销时间**裁决：先核销 → 享折扣（哪怕之后被封）；先封禁 → 原价支付 |
| 同一码并发核销 | 条件 UPDATE（`redeemed_at IS NULL AND banned_at IS NULL`）抢占，恰好一单成功，另一单原价 |
| 一笔订单一个折扣 | 折扣只有 `discount_fen` 一列，重核价在单条 UPDATE 中改写，不存在两个折扣金额 |
| 重复支付回执 | 按回执号幂等：重复投递返回同一结果，不重复扣款 / 不重复核销；回执串单则拒绝 |
| 误封恢复 | 仅限**仍在有效期 + 从未核销**的码；已核销码永不重新开放，过期批次不可恢复 |

## 关键实现

- `backend/internal/pricing/pricing.go` — 纯函数核价（会员/商品/有效期/封禁/核销状态），有效期半开区间 `[from, until)`
- `backend/internal/service/actions.go`
  - `BanCodes`：单事务内封码 + 重核价待付款单 + 收集已付款单
  - `Pay`：单事务内核验回执幂等 → 服务端时刻重新核价 → 条件 UPDATE 抢占核销 → 落单 + 写回执
  - SQLite 连接池 `SetMaxOpenConns(1)`，事务天然串行，杜绝交错结果
- 金额全部以**分（int64）**存储，避免浮点误差

## API

```
GET  /api/products /api/members /api/batches
GET  /api/codes?batch_id=            # 仅尾号
POST /api/codes/:id/ban              {"reason":"..."}
POST /api/codes/ban-batch/:batchID   整批封禁
POST /api/codes/:id/unban            误封恢复（409: already_redeemed / out_of_validity）
POST /api/orders/quote               核价预览
POST /api/orders                     下单（码不可用 → 409 + reason）
POST /api/orders/:no/pay             {"receipt_no":"可选"}  # 留空本地模拟
GET  /api/orders /api/orders/:no
GET  /api/impact                     影响清单（已付款 + 码被封，带原优惠快照）
```

## 验证

```bash
cd backend && go test -race ./...
```

覆盖：封禁前后核价边界（有效期两端、封禁/核销状态）、待付款重核价、已付款快照入影响清单、
同码并发核销只成功一次（-count 多轮 + race）、重复回执幂等、回执串单拒绝、
误封恢复三条件（未核销 / 有效期内 / 过期拒绝）、列表不泄漏明文码、HTTP 全生命周期。
