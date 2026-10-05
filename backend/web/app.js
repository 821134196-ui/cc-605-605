const { createApp } = Vue;

createApp({
  data() {
    return {
      tab: 'checkout',
      tabs: [
        { key: 'checkout', label: '结账下单' },
        { key: 'codes', label: '折扣码管理' },
        { key: 'orders', label: '订单追溯' },
        { key: 'impact', label: '泄露影响清单' },
      ],
      members: [], products: [], batches: [], codes: [], orders: [], impact: [],
      codeFilter: 0,
      cart: { member_id: 1, qty: {}, code: '' },
      quote: null,
      currentOrder: null,
      payReceipt: '',
      detail: null,
      toast: '', toastKind: '', toastTimer: null,
    };
  },
  computed: {
    // 没有真正的计算需求，保留扩展位
  },
  async mounted() {
    await Promise.all([this.loadMembers(), this.loadProducts(), this.loadBatches()]);
    this.cart.member_id = this.members[0]?.id || 1;
    this.products.forEach(p => { this.cart.qty[p.id] = 0; });
    await this.loadCodes();
  },
  methods: {
    // ---------- utils ----------
    fen(v) { return (Number(v || 0) / 100).toFixed(2); },
    fmt(t) { return t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'; },
    levelName(l) { return { gold: '金卡', silver: '银卡', normal: '普通' }[l] || l; },
    statusName(s) { return { pending: '待付款', paid: '已付款', cancelled: '已取消' }[s] || s; },
    codeStatus(s) {
      return { active: '可用', banned: '已封禁', redeemed: '已核销', expired: '已过期' }[s] || s;
    },
    reasonName(r) {
      return {
        code_not_found: '码不存在', code_banned: '码已封禁', code_redeemed: '码已被核销',
        code_not_started: '未到生效时间', code_expired: '已过有效期',
        member_not_applicable: '会员等级不适用', empty_basket: '无适用商品',
      }[r] || r;
    },
    ruleText(c) {
      if (c.discount_type === 'percent') return `适用商品 ${c.discount_value}% off`;
      return `立减 ¥${this.fen(c.discount_value)}`;
    },
    snapRule(p) {
      if (p.discount_type === 'percent') return `减 ${p.discount_value}%`;
      return `立减 ¥${this.fen(p.discount_value)}`;
    },
    cartItems() {
      return this.products
        .filter(p => (this.cart.qty[p.id] || 0) > 0)
        .map(p => ({ product_id: p.id, qty: Number(this.cart.qty[p.id]) }));
    },
    notify(msg, kind = '') {
      this.toast = msg; this.toastKind = kind;
      clearTimeout(this.toastTimer);
      this.toastTimer = setTimeout(() => { this.toast = ''; }, 3200);
    },
    async api(path, opts = {}) {
      const res = await fetch('/api' + path, {
        headers: { 'Content-Type': 'application/json' },
        ...opts,
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error + (body.reason ? `（${this.reasonName(body.reason)}）` : ''));
      return body;
    },

    // ---------- loads ----------
    async loadMembers() { this.members = await this.api('/members'); },
    async loadProducts() { this.products = await this.api('/products'); },
    async loadBatches() { this.batches = await this.api('/batches'); },
    async loadCodes() { this.codes = await this.api('/codes' + (this.codeFilter ? `?batch_id=${this.codeFilter}` : '')); },
    async loadOrders() { this.orders = await this.api('/orders'); },
    async loadImpact() { this.impact = await this.api('/impact'); },

    // ---------- checkout ----------
    async doQuote() {
      try {
        const r = await this.api('/orders/quote', {
          method: 'POST',
          body: JSON.stringify({ member_id: this.cart.member_id, items: this.cartItems(), code: this.cart.code.trim() }),
        });
        this.quote = r.quote;
      } catch (e) { this.notify(e.message, 'err'); }
    },
    async doCreate() {
      try {
        const o = await this.api('/orders', {
          method: 'POST',
          body: JSON.stringify({ member_id: this.cart.member_id, items: this.cartItems(), code: this.cart.code.trim() }),
        });
        this.currentOrder = o;
        this.quote = null;
        this.notify(`已生成待付款单 ${o.order_no}`, 'ok');
        this.loadOrders();
      } catch (e) { this.notify(e.message, 'err'); }
    },
    async doPay() {
      try {
        const r = await this.api(`/orders/${this.currentOrder.order_no}/pay`, {
          method: 'POST',
          body: JSON.stringify({ receipt_no: this.payReceipt.trim() }),
        });
        this.currentOrder = r.order;
        this.payReceipt = '';
        this.notify(r.idempotent ? '重复回执：幂等返回，未重复扣款/核销' : '支付核销成功', r.idempotent ? '' : 'ok');
        this.loadCodes(); this.loadOrders(); this.loadImpact();
      } catch (e) { this.notify(e.message, 'err'); }
    },
    async showOrder(o) {
      try {
        this.detail = await this.api(`/orders/${o.order_no}`);
        if (this.tab !== 'orders') this.tab = 'orders';
      } catch (e) { this.notify(e.message, 'err'); }
    },

    // ---------- code ops ----------
    async banOne(c) {
      const reason = prompt(`封禁码尾号 ${c.code_tail}，填写泄露原因：`, '外部渠道截图泄露');
      if (reason === null) return;
      await this.api(`/codes/${c.id}/ban`, { method: 'POST', body: JSON.stringify({ reason }) });
      this.notify('已封禁；待付款单已重新核价，已付款单进入影响清单', 'ok');
      await this.loadCodes(); await this.loadOrders(); await this.loadImpact();
      if (this.currentOrder) this.currentOrder = this.orders.find(o => o.order_no === this.currentOrder.order_no) || this.currentOrder;
    },
    async unbanOne(c) {
      try {
        await this.api(`/codes/${c.id}/unban`, { method: 'POST', body: '{}' });
        this.notify('误封已恢复', 'ok');
      } catch (e) { this.notify('恢复失败：' + e.message, 'err'); }
      await this.loadCodes();
    },
    async banBatch() {
      if (!this.codeFilter) { this.notify('请先选择一个批次再整批封禁', 'err'); return; }
      const reason = prompt('整批封禁原因：', '批次码在二手平台泄露');
      if (reason === null) return;
      const res = await this.api(`/codes/ban-batch/${this.codeFilter}`, {
        method: 'POST', body: JSON.stringify({ reason }),
      });
      this.notify(`封禁 ${res.banned_code_ids.length} 个码；重核价待付款单 ${res.repriced_orders.length} 笔；已付款保留 ${res.paid_orders_kept.length} 笔`, 'ok');
      await this.loadCodes(); await this.loadOrders(); await this.loadImpact();
    },
  },
  watch: {
    tab(t) {
      if (t === 'orders') this.loadOrders();
      if (t === 'impact') this.loadImpact();
    },
  },
}).mount('#app');
