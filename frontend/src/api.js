const BASE = '/api'

async function req(path, opts = {}) {
  const res = await fetch(BASE + path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (!res.ok) {
    const err = new Error(data?.error || `HTTP ${res.status}`)
    err.status = res.status
    err.data = data
    throw err
  }
  return data
}

export const api = {
  products: () => req('/products'),
  batches: () => req('/batches'),
  banBatch: (id, reason) => req(`/batches/${id}/ban`, { method: 'POST', body: { reason } }),
  restoreBatch: (id) => req(`/batches/${id}/restore`, { method: 'POST', body: {} }),
  issueCode: (id) => req(`/batches/${id}/codes`, { method: 'POST', body: {} }),
  codes: (batchId) => req('/codes' + (batchId ? `?batch_id=${batchId}` : '')),
  banCode: (id, reason) => req(`/codes/${id}/ban`, { method: 'POST', body: { reason } }),
  restoreCode: (id) => req(`/codes/${id}/restore`, { method: 'POST', body: {} }),
  quote: (body) => req('/quote', { method: 'POST', body }),
  createOrder: (body) => req('/orders', { method: 'POST', body }),
  orders: () => req('/orders'),
  order: (id) => req(`/orders/${id}`),
  requote: (id) => req(`/orders/${id}/requote`, { method: 'POST', body: {} }),
  pay: (id, body = {}) => req(`/orders/${id}/pay`, { method: 'POST', body }),
  impact: (batchId, codeId) => {
    const q = new URLSearchParams()
    if (batchId) q.set('batch_id', batchId)
    if (codeId) q.set('code_id', codeId)
    const s = q.toString()
    return req('/impact' + (s ? '?' + s : ''))
  },
}

// 金额：分 → 元
export function yuan(cents) {
  if (cents == null) return '—'
  return (Number(cents) / 100).toFixed(2)
}

export function fmtTime(t) {
  if (!t) return '—'
  return new Date(t).toLocaleString('zh-CN', { hour12: false })
}
