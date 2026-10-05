<script setup>
import { inject, onMounted, ref } from 'vue'
import { api, yuan } from '../api'

const toast = inject('toast')
const batches = ref([])
const validBatches = ref([])
const batchId = ref(null)
const N = ref(12)
const running = ref(false)
const log1 = ref('')
const log2 = ref('')
const log3 = ref('')
const raceRounds = ref(10)

onMounted(async () => {
  batches.value = await api.batches()
  // 可演练批次：未封禁且当前在有效期内
  validBatches.value = batches.value.filter((b) => b.state === 'active' && new Date(b.valid_until) > new Date())
  batchId.value = validBatches.value[0]?.id || null
})

function logLine(r, s) {
  const t = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  r.value += `[${t}] ${s}\n`
}
function pickBatch() {
  const b = batches.value.find((x) => x.id === batchId.value)
  return b
}
async function orderContext(b) {
  const products = await api.products()
  const level = b.member_levels?.length ? b.member_levels[0] : 'normal'
  const pid = b.product_ids?.length ? b.product_ids[0] : products[0].id
  return { level, pid }
}
async function newOrderWithCode(issue) {
  const b = pickBatch()
  const { level, pid } = await orderContext(b)
  return api.createOrder({ member_level: level, code: issue.full_code, items: [{ product_id: pid, qty: 1 }] })
}

// 演练 1：同码并发核销只能成功一次
async function raceRedeem() {
  const b = pickBatch()
  if (!b) return toast.err('请先选择一个生效中的批次')
  running.value = true
  log1.value = ''
  try {
    const issue = await api.issueCode(b.id)
    const ids = []
    for (let i = 0; i < N.value; i++) ids.push((await newOrderWithCode(issue)).id)
    logLine(log1, `发放新码（尾号 ${issue.tail}），创建 ${ids.length} 笔待付款订单，全部同时发起支付…`)
    const t0 = performance.now()
    const results = await Promise.all(
      ids.map((id, i) =>
        api.pay(id, { receipt_no: `LAB1-${issue.tail}-${i}` }).then(
          (r) => ({ id, ok: true, r }),
          (e) => ({ id, ok: false, e: e.message })
        )
      )
    )
    const success = results.filter((r) => r.ok)
    const failed = results.filter((r) => !r.ok)
    logLine(log1, `并发结束，耗时 ${(performance.now() - t0).toFixed(0)}ms`)
    logLine(log1, `支付成功 ${success.length} 笔（应为 1）：订单 ${success.map((r) => '#' + r.id).join(', ')}`)
    logLine(log1, `被拒绝 ${failed.length} 笔（应为 ${ids.length - 1}），原因：${failed[0]?.e || ''}`)
    const w = success[0]?.r
    if (w) logLine(log1, `唯一核销：折扣 −¥${yuan(w.payment.discount_amount)}，实付 ¥${yuan(w.payment.paid_amount)}，码置 redeemed`)
    logLine(log1, success.length === 1 && failed.length === ids.length - 1 ? '✔ 同一码并发核销仅成功一次' : '✘ 结果不符合预期！')
  } catch (e) {
    logLine(log1, '✘ ' + e.message)
  } finally {
    running.value = false
  }
}

// 演练 2：支付 vs 封禁同时发生——多轮，结果由服务端核销顺序裁决
async function racePayVsBan() {
  const b = pickBatch()
  if (!b) return toast.err('请先选择一个生效中的批次')
  running.value = true
  log2.value = ''
  try {
    let discountWins = 0
    let fullWins = 0
    const anomalies = []
    for (let round = 1; round <= raceRounds.value; round++) {
      const issue = await api.issueCode(b.id)
      const o = await newOrderWithCode(issue)
      // 支付与“封禁这张刚发的码”在同一批次并发发出，不预设先后
      const [payOut, banOut] = await Promise.all([
        api.pay(o.id, { receipt_no: `LAB2-${issue.tail}` }).then(
          (r) => ({ status: 'fulfilled', r }),
          (e) => ({ status: 'rejected', e: e.message })
        ),
        api.banCode(issue.id, '演练竞争').then(
          () => ({ status: 'fulfilled' }),
          (e) => ({ status: 'rejected', e: e.message })
        ),
      ])
      const detail = await api.order(o.id)
      const p = detail.payment
      const consistent =
        p && detail.order.discount_amount === p.discount_amount && detail.order.final_amount === p.paid_amount
      if (payOut.status === 'fulfilled' && p) {
        if (p.discount_amount > 0 && p.code_id) {
          discountWins++
          logLine(log2, `第${String(round).padStart(2)}轮：支付先核销 → 优惠 −¥${yuan(p.discount_amount)}，实付 ¥${yuan(p.paid_amount)}（封禁随后，订单已付款不改）`)
        } else {
          fullWins++
          logLine(log2, `第${String(round).padStart(2)}轮：封禁先生效 → 折扣剥离，原价 ¥${yuan(p.paid_amount)} 收款，未核销码`)
        }
        if (!consistent) anomalies.push(`第${round}轮订单/回执金额不一致`)
      } else {
        anomalies.push(`第${round}轮支付被拒：${payOut.e}`)
        logLine(log2, `第${String(round).padStart(2)}轮：支付被拒（${payOut.e}）封禁结果=${banOut.status}`)
      }
    }
    logLine(log2, `—— 合计：支付抢先 ${discountWins} 笔，封禁抢先 ${fullWins} 笔`)
    logLine(log2, anomalies.length ? '✘ ' + anomalies.join('；') : '✔ 每笔订单恰好一条回执、一个折扣金额，订单与回执金额完全一致')
  } catch (e) {
    logLine(log2, '✘ ' + e.message)
  } finally {
    running.value = false
  }
}

// 演练 3：重复支付回执幂等
async function duplicateReceipt() {
  const b = pickBatch()
  if (!b) return toast.err('请先选择一个生效中的批次')
  running.value = true
  log3.value = ''
  try {
    const issue = await api.issueCode(b.id)
    const o = await newOrderWithCode(issue)
    const receipt = `LAB3-DUP-${issue.tail}`
    logLine(log3, `订单 #${o.id}（尾号 ${issue.tail}）首次提交回执 ${receipt}`)
    const r1 = await api.pay(o.id, { receipt_no: receipt })
    logLine(log3, `首次：实付 ¥${yuan(r1.payment.paid_amount)} 折扣 −¥${yuan(r1.payment.discount_amount)} 回执ID=${r1.payment.id} 幂等重放=${r1.idempotent_replay}`)

    logLine(log3, '支付渠道重复推送同一回执（再次提交）…')
    const r2 = await api.pay(o.id, { receipt_no: receipt })
    logLine(log3, `重复：回执ID=${r2.payment.id}（与首次相同=${r2.payment.id === r1.payment.id}）幂等重放=${r2.idempotent_replay} 金额仍 ¥${yuan(r2.payment.paid_amount)}`)

    logLine(log3, '不带回执号再通知一次该已付款订单…')
    const r3 = await api.pay(o.id, {})
    logLine(log3, `结果：幂等重放=${r3.idempotent_replay}，回执ID=${r3.payment.id}`)

    const issue2 = await api.issueCode(b.id)
    const o2 = await newOrderWithCode(issue2)
    let conflict = ''
    try {
      await api.pay(o2.id, { receipt_no: receipt })
    } catch (e) {
      conflict = e.message
    }
    logLine(log3, `同一回执号串到另一订单 → ${conflict || '未拒绝（异常！）'}`)
    const ok = r2.payment.id === r1.payment.id && r2.idempotent_replay && r3.idempotent_replay && conflict
    logLine(log3, ok ? '✔ 重复回执不二次扣款、不产生第二条记录；串单被拒绝' : '✘ 幂等验证失败')
  } catch (e) {
    log3.value += '✘ ' + e.message + '\n'
  } finally {
    running.value = false
  }
}
</script>

<template>
  <div class="panel">
    <h2>并发与异常演练</h2>
    <div class="row">
      <div class="field">
        <label>演练使用的生效批次（每次演练自动发放全新码，可反复运行）</label>
        <select v-model="batchId">
          <option v-for="b in validBatches" :key="b.id" :value="b.id">#{{ b.id }} {{ b.name }}</option>
        </select>
      </div>
      <small class="hint">若无可选批次，先到「批次与折扣码」页解封一个仍在有效期的批次。</small>
    </div>
  </div>

  <div class="grid2">
    <div class="panel">
      <h2>① 同一码并发核销</h2>
      <div class="row">
        <div class="field"><label>并发订单数</label><input type="number" min="2" max="40" v-model.number="N" /></div>
        <button class="btn primary" :disabled="running" @click="raceRedeem">同时发起 N 笔支付</button>
      </div>
      <div class="race-log" style="margin-top:12px">{{ log1 || '点击按钮：N 笔订单使用同一张新码，浏览器同一时刻并发提交支付，预期仅 1 笔成功。' }}</div>
    </div>

    <div class="panel">
      <h2>② 支付 × 封禁 同时发生</h2>
      <div class="row">
        <div class="field"><label>轮次</label><input type="number" min="2" max="30" v-model.number="raceRounds" /></div>
        <button class="btn warn" :disabled="running" @click="racePayVsBan">并发对撞</button>
      </div>
      <p class="muted" style="margin:8px 0">每轮一张新码、一笔待付款订单；支付请求与封禁请求同批发出，由<strong>服务端核销时间</strong>裁决：先支付→保留优惠；先封禁→剥离折扣按原价。</p>
      <div class="race-log">{{ log2 || '结果二选一，但任何一轮都不会出现两个折扣金额或两条回执。' }}</div>
    </div>
  </div>

  <div class="panel">
    <h2>③ 重复支付回执</h2>
    <button class="btn ok" :disabled="running" @click="duplicateReceipt">支付 → 重复同回执 → 再次通知 → 串单</button>
    <div class="race-log" style="margin-top:12px">{{ log3 || '验证：同一渠道回执重复到达只落一条记录、不二次扣款；回执串到别的订单会被拒绝。' }}</div>
  </div>
</template>
