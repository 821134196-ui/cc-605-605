<script setup>
import { provide, reactive, ref } from 'vue'
import BatchesCodes from './components/BatchesCodes.vue'
import OrderDesk from './components/OrderDesk.vue'
import Orders from './components/Orders.vue'
import Impact from './components/Impact.vue'
import Lab from './components/Lab.vue'

const tabs = [
  { key: 'desk', name: '核价与下单' },
  { key: 'codes', name: '批次与折扣码' },
  { key: 'orders', name: '订单与支付回执' },
  { key: 'impact', name: '影响清单追溯' },
  { key: 'lab', name: '并发演练' },
]
const tab = ref('desk')

// 全局轻提示
const toasts = reactive([])
let tid = 0
function push(msg, type = 'ok', ms = 3800) {
  const id = ++tid
  toasts.push({ id, msg, type })
  setTimeout(() => {
    const i = toasts.findIndex((t) => t.id === id)
    if (i >= 0) toasts.splice(i, 1)
  }, ms)
}
provide('toast', {
  ok: (m) => push(m, 'ok'),
  err: (m) => push(m, 'err', 6000),
})
</script>

<template>
  <div class="app">
    <header class="top">
      <h1>门店折扣码泄露封禁与订单追溯系统</h1>
      <span class="sub">封禁即重新核价 · 已付款保留快照 · 核销按服务端时间唯一裁决</span>
    </header>

    <nav class="tabs">
      <button v-for="t in tabs" :key="t.key" :class="{ active: tab === t.key }" @click="tab = t.key">
        {{ t.name }}
      </button>
    </nav>

    <OrderDesk v-if="tab === 'desk'" />
    <BatchesCodes v-else-if="tab === 'codes'" />
    <Orders v-else-if="tab === 'orders'" />
    <Impact v-else-if="tab === 'impact'" />
    <Lab v-else-if="tab === 'lab'" />

    <div class="toast">
      <div v-for="t in toasts" :key="t.id" class="t" :class="t.type">{{ t.msg }}</div>
    </div>
  </div>
</template>
