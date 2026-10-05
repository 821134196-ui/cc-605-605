<script setup>
import { inject, onMounted, ref } from 'vue'
import { api, fmtTime, yuan } from '../api'

const toast = inject('toast')
const batches = ref([])
const filterBatch = ref('')
const entries = ref([])
const openId = ref(null)

async function load() {
  try {
    entries.value = await api.impact(filterBatch.value ? Number(filterBatch.value) : 0, 0)
  } catch (e) {
    toast.err(e.message)
  }
}
onMounted(async () => {
  batches.value = await api.batches()
  await load()
})

function ruleDesc(r) {
  if (!r) return '—'
  return r.discount_type === 'percent'
    ? `立减 ${r.discount_value}%（适用 ¥${yuan(r.eligible_amount)}）`
    : `立减 ¥${yuan(r.discount_value)}（适用 ¥${yuan(r.eligible_amount)}）`
}
</script>

<template>
  <div class="panel">
    <div class="row" style="justify-content:space-between">
      <div>
        <h2 style="margin:0">影响清单：已付款且优惠事后被封禁的订单</h2>
        <p class="muted" style="margin:6px 0 0">
          这些订单保留<strong>支付当时确认</strong>的优惠规则与金额快照（不追扣、不改价），供运营逐笔追溯。
          判定来源：支付回执上真实核销了折扣码（被剥离折扣、按原价支付的订单不在清单内）。
        </p>
      </div>
      <div class="row">
        <div class="field">
          <label>批次筛选</label>
          <select v-model="filterBatch" @change="load">
            <option value="">全部批次</option>
            <option v-for="b in batches" :key="b.id" :value="b.id">#{{ b.id }} {{ b.name }}</option>
          </select>
        </div>
        <button class="btn" @click="load">刷新</button>
      </div>
    </div>

    <div class="row" style="margin:14px 0">
      <div class="stat"><div class="l">受影响已付款订单</div><div class="n">{{ entries.length }}</div></div>
      <div class="stat"><div class="l">涉及优惠总额</div><div class="n amount discount">¥{{ yuan(entries.reduce((s, e) => s + (e.payment?.discount_amount || 0), 0)) }}</div></div>
      <div class="stat"><div class="l">当时实付总额</div><div class="n">¥{{ yuan(entries.reduce((s, e) => s + (e.payment?.paid_amount || 0), 0)) }}</div></div>
    </div>

    <table>
      <thead>
        <tr>
          <th>订单</th><th>批次</th><th>码尾号</th><th>封禁级别</th><th>封禁时间/原因</th>
          <th>原价</th><th>当时优惠</th><th>当时实付</th><th>支付核销时间</th><th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="e in entries" :key="e.order.id">
          <td>
            <div class="mono">{{ e.order.order_no }}</div>
            <div class="muted">#{{ e.order.id }} · {{ e.order.member_level }}</div>
          </td>
          <td>#{{ e.batch_id }} {{ e.batch_name }}</td>
          <td class="mono">{{ e.payment?.applied_rule?.code_tail || e.order.code_tail }}</td>
          <td><span class="tag banned">{{ e.banned_scope === 'batch' ? '整批封禁' : '单码封禁' }}</span></td>
          <td class="muted" style="white-space:nowrap">{{ fmtTime(e.banned_at) }}<br />{{ e.ban_reason }}</td>
          <td>¥{{ yuan(e.order.original_amount) }}</td>
          <td class="amount discount">−¥{{ yuan(e.payment?.discount_amount) }}</td>
          <td class="amount">¥{{ yuan(e.payment?.paid_amount) }}</td>
          <td class="muted" style="white-space:nowrap">{{ fmtTime(e.payment?.created_at) }}</td>
          <td><button class="btn" @click="openId = openId === e.order.id ? null : e.order.id">原优惠快照</button></td>
        </tr>
        <tr v-if="!entries.length">
          <td colspan="10" class="muted" style="text-align:center;padding:24px">暂无受影响订单。</td>
        </tr>
      </tbody>
    </table>

    <div v-for="e in entries" :key="'d' + e.order.id">
      <div v-if="openId === e.order.id" class="panel" style="background:var(--panel2);margin-top:10px">
        <h3>订单 {{ e.order.order_no }} 的原优惠快照（支付时冻结）</h3>
        <div class="grid2">
          <dl class="kv" v-if="e.payment?.applied_rule">
            <dt>批次</dt><dd>#{{ e.payment.applied_rule.batch_id }} {{ e.payment.applied_rule.batch_name }}</dd>
            <dt>码尾号</dt><dd class="mono">{{ e.payment.applied_rule.code_tail }}</dd>
            <dt>规则</dt><dd>{{ ruleDesc(e.payment.applied_rule) }}</dd>
            <dt>门槛</dt><dd>¥{{ yuan(e.payment.applied_rule.min_amount) }}</dd>
            <dt>适用会员</dt><dd>{{ (e.payment.applied_rule.member_levels || []).join('/') || '全部' }}</dd>
            <dt>适用商品</dt><dd>{{ (e.payment.applied_rule.product_ids || []).length ? '#' + e.payment.applied_rule.product_ids.join(', #') : '全部商品' }}</dd>
            <dt>快照时间</dt><dd>{{ fmtTime(e.payment.applied_rule.evaluated_at) }}</dd>
          </dl>
          <dl class="kv">
            <dt>回执号</dt><dd class="mono">{{ e.payment?.receipt_no }}</dd>
            <dt>服务端核销时间</dt><dd>{{ fmtTime(e.payment?.created_at) }}</dd>
            <dt>原价</dt><dd>¥{{ yuan(e.payment?.original_amount) }}</dd>
            <dt>优惠金额</dt><dd class="amount discount">−¥{{ yuan(e.payment?.discount_amount) }}</dd>
            <dt>实付金额</dt><dd class="amount">¥{{ yuan(e.payment?.paid_amount) }}</dd>
            <dt>商品</dt>
            <dd>
              <span v-for="(it, i) in e.order.items" :key="i">
                {{ it.name }}×{{ it.qty }}<template v-if="i < e.order.items.length - 1">，</template>
              </span>
            </dd>
          </dl>
        </div>
      </div>
    </div>
  </div>
</template>
