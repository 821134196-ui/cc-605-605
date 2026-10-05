<script setup>
import { inject, onMounted, ref } from 'vue'
import { api, fmtTime, yuan } from '../api'

const toast = inject('toast')
const orders = ref([])
const detail = ref(null)
const receiptNo = ref('')
const payAmountMode = ref('auto') // auto | manual
const manualAmount = ref('')
const busy = ref(false)

async function load() {
  orders.value = await api.orders()
}
onMounted(load)

async function open(o) {
  try {
    detail.value = await api.order(o.id)
    receiptNo.value = ''
    payAmountMode.value = 'auto'
    manualAmount.value = ''
  } catch (e) {
    toast.err(e.message)
  }
}
function close() {
  detail.value = null
}

async function requote() {
  busy.value = true
  try {
    const r = await api.requote(detail.value.order.id)
    detail.value = await api.order(detail.value.order.id)
    toast.ok(r.changed ? '已按当前状态重新核价：折扣发生变化（详见价格历史）' : '重新核价完成：金额无变化')
  } catch (e) {
    toast.err(e.message)
  } finally {
    busy.value = false
  }
}

async function pay() {
  busy.value = true
  try {
    const body = { receipt_no: receiptNo.value.trim() }
    if (payAmountMode.value === 'manual') body.paid_amount = Math.round(Number(manualAmount.value) * 100)
    const r = await api.pay(detail.value.order.id, body)
    if (r.idempotent_replay) toast.ok('重复回执：幂等返回原回执，未发生第二次扣款')
    else {
      toast.ok(
        `支付成功 ¥${yuan(r.payment.paid_amount)}` +
          (r.requoted_at_pay ? '（支付时刻重新核价，折扣已按当前状态调整）' : '') +
          (r.payment.code_id ? '，码已核销' : '，未核销码')
      )
    }
    detail.value = await api.order(detail.value.order.id)
    await load()
  } catch (e) {
    toast.err('支付失败：' + e.message)
  } finally {
    busy.value = false
  }
}

function ruleDesc(r) {
  if (!r) return '—'
  return r.discount_type === 'percent'
    ? `立减 ${r.discount_value}%（适用金额 ¥${yuan(r.eligible_amount)}）`
    : `立减 ¥${yuan(r.discount_value)}（适用金额 ¥${yuan(r.eligible_amount)}）`
}
</script>

<template>
  <div class="panel">
    <div class="row" style="justify-content:space-between">
      <h2 style="margin:0">订单与本地模拟支付回执</h2>
      <button class="btn" @click="load">刷新</button>
    </div>
    <table>
      <thead>
        <tr>
          <th>#</th><th>订单号</th><th>会员</th><th>状态</th><th>原价</th><th>折扣</th><th>应付</th>
          <th>码尾号</th><th>创建</th><th>支付</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="o in orders" :key="o.id" class="clickable" @click="open(o)">
          <td>{{ o.id }}</td>
          <td class="mono">{{ o.order_no }}</td>
          <td>{{ o.member_level }}</td>
          <td><span class="tag" :class="o.status">{{ o.status === 'paid' ? '已付款' : '待付款' }}</span>
            <div v-if="o.status==='pending' && o.reject_reason" class="muted">{{ o.reject_reason }}</div>
          </td>
          <td>¥{{ yuan(o.original_amount) }}</td>
          <td class="amount discount">−¥{{ yuan(o.discount_amount) }}</td>
          <td class="amount">¥{{ yuan(o.final_amount) }}</td>
          <td class="mono">{{ o.code_tail || '—' }}</td>
          <td class="muted" style="white-space:nowrap">{{ fmtTime(o.created_at) }}</td>
          <td class="muted" style="white-space:nowrap">{{ fmtTime(o.paid_at) }}</td>
        </tr>
      </tbody>
    </table>
  </div>

  <div v-if="detail" class="modal-mask" @click.self="close">
    <div class="modal">
      <div class="row" style="justify-content:space-between">
        <h2>订单 #{{ detail.order.id }} <span class="tag" :class="detail.order.status">{{ detail.order.status === 'paid' ? '已付款' : '待付款' }}</span></h2>
        <button class="btn" @click="close">关闭</button>
      </div>
      <p class="mono muted">{{ detail.order.order_no }} ｜ {{ detail.order.member_level }}</p>

      <div class="grid2">
        <div>
          <h3>商品明细（快照）</h3>
          <table>
            <tbody>
              <tr v-for="(it, i) in detail.order.items" :key="i">
                <td>{{ it.name }}</td><td>¥{{ yuan(it.unit_price) }} × {{ it.qty }}</td><td>¥{{ yuan(it.line_total) }}</td>
              </tr>
            </tbody>
          </table>
          <h3>金额（一笔订单只有一个折扣金额）</h3>
          <dl class="kv">
            <dt>原价</dt><dd>¥{{ yuan(detail.order.original_amount) }}</dd>
            <dt>折扣</dt><dd class="amount discount">−¥{{ yuan(detail.order.discount_amount) }}</dd>
            <dt>应付/实付</dt><dd class="amount">¥{{ yuan(detail.order.final_amount) }}</dd>
            <dt>码尾号</dt><dd class="mono">{{ detail.order.code_tail || '—' }}</dd>
          </dl>
        </div>
        <div>
          <h3>采用的优惠规则快照</h3>
          <template v-if="detail.order.applied_rule">
            <dl class="kv">
              <dt>批次</dt><dd>#{{ detail.order.applied_rule.batch_id }} {{ detail.order.applied_rule.batch_name }}</dd>
              <dt>码尾号</dt><dd class="mono">{{ detail.order.applied_rule.code_tail }}</dd>
              <dt>规则</dt><dd>{{ ruleDesc(detail.order.applied_rule) }}</dd>
              <dt>门槛</dt><dd>¥{{ yuan(detail.order.applied_rule.min_amount) }}</dd>
              <dt>适用会员</dt><dd>{{ (detail.order.applied_rule.member_levels || []).join('/') || '全部' }}</dd>
              <dt>快照时间</dt><dd>{{ fmtTime(detail.order.applied_rule.evaluated_at) }}</dd>
            </dl>
          </template>
          <p v-else class="muted">当前无生效优惠规则。{{ detail.order.reject_reason ? '未生效原因：' + detail.order.reject_reason : '' }}</p>
          <p class="muted" v-if="detail.order.status === 'paid'">已付款：金额与规则快照冻结，任何封禁都不会改写本单。</p>
        </div>
      </div>

      <h3>核价历史（只追加）</h3>
      <table>
        <thead><tr><th>时间</th><th>原因</th><th>原价</th><th>折扣</th><th>应付</th><th>规则</th></tr></thead>
        <tbody>
          <tr v-for="h in detail.price_history" :key="h.id">
            <td class="muted" style="white-space:nowrap">{{ fmtTime(h.evaluated_at) }}</td>
            <td><span class="tag" :class="h.reason === 'BAN_STRIPPED' ? 'banned' : 'active'">{{ h.reason }}</span></td>
            <td>¥{{ yuan(h.original_amount) }}</td>
            <td class="amount discount">−¥{{ yuan(h.discount_amount) }}</td>
            <td class="amount">¥{{ yuan(h.final_amount) }}</td>
            <td class="muted">{{ h.applied_rule ? ruleDesc(h.applied_rule) + ' / 尾号 ' + h.applied_rule.code_tail : '无（原价）' }}</td>
          </tr>
        </tbody>
      </table>

      <h3>支付回执</h3>
      <template v-if="detail.payment">
        <dl class="kv">
          <dt>回执号</dt><dd class="mono">{{ detail.payment.receipt_no }}</dd>
          <dt>核销时间</dt><dd>{{ fmtTime(detail.payment.created_at) }}（服务端时间，裁决支付与封禁先后）</dd>
          <dt>原价/折扣/实付</dt><dd>¥{{ yuan(detail.payment.original_amount) }} / −¥{{ yuan(detail.payment.discount_amount) }} / <strong>¥{{ yuan(detail.payment.paid_amount) }}</strong></dd>
          <dt>核销码</dt><dd>{{ detail.payment.code_id ? '码 #' + detail.payment.code_id : '无（按原价收款）' }}</dd>
        </dl>
      </template>
      <p v-else class="muted">暂无回执。</p>

      <div v-if="detail.order.status === 'pending'" style="margin-top:14px">
        <h3>模拟支付</h3>
        <div class="row">
          <div class="field" style="min-width:240px">
            <label>支付渠道回执号（留空自动生成；填同一号重发=模拟重复回执）</label>
            <input class="wide mono" v-model="receiptNo" placeholder="如 CHANNEL-RCP-10001" />
          </div>
          <div class="field">
            <label>回执金额</label>
            <select v-model="payAmountMode">
              <option value="auto">按服务端当前应付金额（推荐）</option>
              <option value="manual">手动指定（验证金额不符被拒）</option>
            </select>
          </div>
          <div class="field" v-if="payAmountMode === 'manual'">
            <label>金额（元）</label>
            <input v-model="manualAmount" placeholder="24.00" />
          </div>
          <button class="btn primary" :disabled="busy" @click="pay">提交支付回执</button>
          <button class="btn warn" :disabled="busy" @click="requote">支付前手动重新核价</button>
        </div>
      </div>
    </div>
  </div>
</template>
