<script setup>
import { inject, onMounted, ref } from 'vue'
import { api, fmtTime, yuan } from '../api'

const toast = inject('toast')
const batches = ref([])
const codes = ref([])
const filterBatch = ref('')
const loading = ref(false)
const newCode = ref(null) // {full_code, tail} 完整码仅展示这一次

async function load() {
  loading.value = true
  try {
    batches.value = await api.batches()
    codes.value = await api.codes(filterBatch.value ? Number(filterBatch.value) : 0)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function ruleText(b) {
  const t = b.discount_type === 'percent' ? `${b.discount_value}% 折扣` : `立减 ¥${yuan(b.discount_value)}`
  const min = b.min_amount ? `，满 ¥${yuan(b.min_amount)}` : ''
  return t + min
}
function scopeText(b) {
  const lv = b.member_levels && b.member_levels.length ? b.member_levels.join('/') : '全部会员'
  const pd = b.product_ids && b.product_ids.length ? `仅限商品 #${b.product_ids.join(',#')}` : '全部商品'
  return `${lv} ｜ ${pd}`
}
async function askReason(action) {
  const reason = prompt(action + '原因（可留空使用默认）：', '渠道反馈折扣码泄露')
  return reason ?? ''
}
async function banBatch(b) {
  const reason = await askReason('封禁批次')
  try {
    const r = await api.banBatch(b.id, reason || '运营封禁')
    toast.ok(`批次已封禁：停用 ${r.banned_codes} 个码；${r.requoted_orders} 笔待付款已重新核价，${r.affected_paid} 笔已付款进入影响清单`)
    await load()
  } catch (e) {
    toast.err(e.message)
  }
}
async function restoreBatch(b) {
  try {
    await api.restoreBatch(b.id)
    toast.ok('批次已解封；已核销的码保持 redeemed 不会重新开放')
    await load()
  } catch (e) {
    toast.err('解封被拒：' + e.message)
  }
}
async function banCode(c) {
  const reason = await askReason('封禁单码')
  try {
    const r = await api.banCode(c.id, reason || '单码泄露')
    toast.ok(`码 ${c.tail} 已封禁；${r.requoted_orders} 笔待付款重新核价，${r.affected_paid} 笔已付款进入影响清单`)
    await load()
  } catch (e) {
    toast.err(e.message)
  }
}
async function restoreCode(c) {
  try {
    await api.restoreCode(c.id)
    toast.ok(`码 ${c.tail} 已恢复使用`)
    await load()
  } catch (e) {
    toast.err('恢复被拒：' + e.message)
  }
}
async function issueCode(b) {
  try {
    newCode.value = await api.issueCode(b.id)
    toast.ok('新码已发放：完整码仅此一次展示，请立即复制')
    await load()
  } catch (e) {
    toast.err(e.message)
  }
}
function copyCode() {
  if (newCode.value) navigator.clipboard?.writeText(newCode.value.full_code)
}
const stateName = { active: '可用', banned: '已封禁', redeemed: '已核销', banned_redeemed: '已核销·后封禁' }
</script>

<template>
  <div class="panel">
    <h2>折扣批次</h2>
    <table>
      <thead>
        <tr>
          <th>#</th><th>批次</th><th>规则</th><th>适用范围</th><th>有效期</th><th>状态</th><th style="width:230px">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="b in batches" :key="b.id">
          <td>{{ b.id }}</td>
          <td><strong>{{ b.name }}</strong><div v-if="b.ban_reason" class="muted">封禁原因：{{ b.ban_reason }}</div></td>
          <td>{{ ruleText(b) }}</td>
          <td class="muted">{{ scopeText(b) }}</td>
          <td class="muted" style="white-space:nowrap">{{ fmtTime(b.valid_from) }}<br />～ {{ fmtTime(b.valid_until) }}</td>
          <td><span class="tag" :class="b.state">{{ b.state === 'banned' ? '已封禁' : '生效中' }}</span></td>
          <td>
            <div class="pill-line">
              <button v-if="b.state === 'active'" class="btn danger" @click="banBatch(b)">封禁批次</button>
              <button v-else class="btn ok" @click="restoreBatch(b)">解除封禁</button>
              <button class="btn" @click="issueCode(b)" :disabled="b.state === 'banned'">发新码</button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>

  <div v-if="newCode" class="panel" style="border-color:var(--warn)">
    <h2>新码发放（完整码只显示这一次）</h2>
    <p>
      <span class="mono" style="font-size:16px">{{ newCode.full_code }}</span>
      <button class="btn" style="margin-left:10px" @click="copyCode">复制</button>
    </p>
    <p class="muted">尾号 {{ newCode.tail }}；此后列表只显示尾号。 <button class="btn" @click="newCode = null">我已保存，关闭</button></p>
  </div>

  <div class="panel">
    <div class="row" style="justify-content:space-between">
      <h2 style="margin:0">折扣码（列表仅显示尾号）</h2>
      <div class="field">
        <label>按批次筛选</label>
        <select v-model="filterBatch" @change="load">
          <option value="">全部批次</option>
          <option v-for="b in batches" :key="b.id" :value="b.id">#{{ b.id }} {{ b.name }}</option>
        </select>
      </div>
    </div>
    <table>
      <thead>
        <tr><th>#</th><th>码（尾号）</th><th>所属批次</th><th>状态</th><th>有效期至</th><th>关联订单</th><th style="width:170px">操作</th></tr>
      </thead>
      <tbody>
        <tr v-for="c in codes" :key="c.id">
          <td>{{ c.id }}</td>
          <td class="mono" style="font-size:15px">{{ c.tail }}</td>
          <td>#{{ c.batch_id }} {{ c.batch_name }}</td>
          <td><span class="tag" :class="c.state">{{ stateName[c.state] }}</span></td>
          <td class="muted">{{ fmtTime(c.valid_until) }}</td>
          <td class="mono muted">{{ c.order_no || '—' }}</td>
          <td>
            <div class="pill-line">
              <button v-if="c.state === 'active'" class="btn danger" @click="banCode(c)">封禁</button>
              <button v-if="c.state === 'banned'" class="btn ok" @click="restoreCode(c)">误封恢复</button>
              <span v-if="c.state === 'redeemed' || c.state === 'banned_redeemed'" class="muted">终态不可恢复</span>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
    <p class="muted" style="margin-top:10px">
      恢复规则：仅「未核销 且 所属批次未封禁 且 仍在有效期」的码可恢复；已核销（redeemed）为终态，任何情况下都不会重新开放。
    </p>
  </div>
</template>
