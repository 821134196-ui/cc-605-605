<script setup>
import { inject, onMounted, reactive, ref } from 'vue'
import { api, yuan } from '../api'

const toast = inject('toast')
const products = ref([])
const memberLevel = ref('normal')
const code = ref('')
const lines = ref([])
const quote = ref(null)
const lastOrder = ref(null)
const busy = ref(false)

function ensureLine() {
  if (!lines.value.length) addLine()
}
function addLine() {
  lines.value.push({ product_id: products.value[0]?.id, qty: 1 })
}
function removeLine(i) {
  lines.value.splice(i, 1)
}
function payload() {
  return {
    member_level: memberLevel.value,
    code: code.value.trim(),
    items: lines.value.filter((l) => l.qty > 0).map((l) => ({ product_id: Number(l.product_id), qty: Number(l.qty) })),
  }
}
async function load() {
  products.value = await api.products()
  ensureLine()
}
onMounted(load)

async function doQuote() {
  busy.value = true
  try {
    quote.value = await api.quote(payload())
  } catch (e) {
    toast.err('核价失败：' + e.message)
  } finally {
    busy.value = false
  }
}
async function createAndQuote() {
  busy.value = true
  try {
    lastOrder.value = await api.createOrder(payload())
    quote.value = {
      original_amount: lastOrder.value.original_amount,
      discount_amount: lastOrder.value.discount_amount,
      final_amount: lastOrder.value.final_amount,
      reason: lastOrder.value.reject_reason || 'OK',
      rule: lastOrder.value.applied_rule,
    }
    toast.ok(`已创建待付款订单 #${lastOrder.value.id} ${lastOrder.value.order_no}`)
  } catch (e) {
    toast.err('下单失败：' + e.message)
  } finally {
    busy.value = false
  }
}
async function payOrder() {
  if (!lastOrder.value) return
  busy.value = true
  try {
    const r = await api.pay(lastOrder.value.id, { receipt_no: '' })
    lastOrder.value = r.order
    toast.ok(
      (r.idempotent_replay ? '重复回执，幂等返回（未二次扣款）' : `支付成功，实付 ¥${yuan(r.payment.paid_amount)}`) +
        (r.payment.code_id ? '，折扣码已核销' : '，未核销折扣码')
    )
  } catch (e) {
    toast.err('支付被拒：' + e.message)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="panel">
    <h2>核价与下单（价格一律以服务端核价为准）</h2>
    <div class="row">
      <div class="field">
        <label>会员等级</label>
        <select v-model="memberLevel">
          <option value="normal">普通会员</option>
          <option value="silver">银卡</option>
          <option value="gold">金卡</option>
        </select>
      </div>
      <div class="field" style="flex:1;min-width:240px">
        <label>折扣码（输入完整码；系统任何列表只回显尾号）</label>
        <input class="wide mono" v-model="code" placeholder="如 NC-2026-8K2Q-47FD（可留空=原价）" />
      </div>
    </div>

    <h3>商品明细</h3>
    <table>
      <thead><tr><th>商品</th><th style="width:110px">数量</th><th style="width:80px"></th></tr></thead>
      <tbody>
        <tr v-for="(l, i) in lines" :key="i">
          <td>
            <select v-model="l.product_id">
              <option v-for="p in products" :key="p.id" :value="p.id">{{ p.name }}（¥{{ yuan(p.price) }}）</option>
            </select>
          </td>
          <td><input type="number" min="1" v-model.number="l.qty" style="min-width:80px" /></td>
          <td><button class="btn" @click="removeLine(i)">删除</button></td>
        </tr>
      </tbody>
    </table>
    <div style="margin-top:10px" class="row">
      <button class="btn" @click="addLine">+ 添加商品</button>
      <button class="btn" :disabled="busy" @click="doQuote">仅核价</button>
      <button class="btn primary" :disabled="busy" @click="createAndQuote">服务端核价并下单（待付款）</button>
      <button v-if="lastOrder && lastOrder.status === 'pending'" class="btn ok" :disabled="busy" @click="payOrder">
        模拟支付回执（按当前服务端金额）
      </button>
    </div>

    <div v-if="quote" style="margin-top:16px">
      <h3>核价结果</h3>
      <div class="grid2">
        <div class="stat"><div class="l">商品原价</div><div class="n">¥{{ yuan(quote.original_amount) }}</div></div>
        <div class="stat">
          <div class="l">折扣金额（唯一）</div>
          <div class="n amount discount">−¥{{ yuan(quote.discount_amount) }}</div>
        </div>
        <div class="stat">
          <div class="l">应付金额</div>
          <div class="n" :class="quote.discount_amount ? 'amount full' : ''">¥{{ yuan(quote.final_amount) }}</div>
        </div>
        <div class="stat">
          <div class="l">核价判定</div>
          <div class="n" style="font-size:16px">
            <span class="tag" :class="quote.rule ? 'active' : 'banned'">{{ quote.reason }}</span>
          </div>
        </div>
      </div>
      <p v-if="!quote.rule" class="muted">本次优惠未生效（{{ quote.reason }}），按原价成交。</p>
      <div v-if="quote.rule">
        <h3>采用的优惠规则快照</h3>
        <dl class="kv">
          <dt>批次</dt><dd>#{{ quote.rule.batch_id }} {{ quote.rule.batch_name }}</dd>
          <dt>码尾号</dt><dd class="mono">{{ quote.rule.code_tail }}</dd>
          <dt>折扣规则</dt>
          <dd>{{ quote.rule.discount_type === 'percent' ? `立减 ${quote.rule.discount_value}%` : `立减 ¥${yuan(quote.rule.discount_value)}` }}，门槛 ¥{{ yuan(quote.rule.min_amount) }}</dd>
          <dt>适用商品金额</dt><dd>¥{{ yuan(quote.rule.eligible_amount) }}</dd>
          <dt>快照时间</dt><dd>{{ new Date(quote.rule.evaluated_at).toLocaleString('zh-CN') }}</dd>
        </dl>
      </div>
    </div>

    <div v-if="lastOrder" class="panel" style="margin-top:16px;background:var(--panel2)">
      <h3>最近订单 #{{ lastOrder.id }} <span class="tag" :class="lastOrder.status">{{ lastOrder.status === 'paid' ? '已付款' : '待付款' }}</span></h3>
      <p class="muted">
        订单号 <span class="mono">{{ lastOrder.order_no }}</span> ｜ 原价 ¥{{ yuan(lastOrder.original_amount) }} ｜
        折扣 ¥{{ yuan(lastOrder.discount_amount) }} ｜ 应付 ¥{{ yuan(lastOrder.final_amount) }}
      </p>
    </div>
  </div>
</template>
