package app_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"discountguard/internal/app"
	"discountguard/internal/models"
	"discountguard/internal/pricing"
	"discountguard/internal/store"
)

type env struct {
	svc *app.Service
	db  *gorm.DB
}

func setup(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: app.NewService(db), db: db}
}

// seedProduct 建商品，返回 id
func (e *env) seedProduct(name string, price int64) uint {
	p := models.Product{Name: name, Price: price}
	if err := e.db.Create(&p).Error; err != nil {
		panic(err)
	}
	return p.ID
}

type batchSpec struct {
	typ     string
	value   int64
	min     int64
	members []string
	pids    []uint
	from    time.Time
	until   time.Time
}

func (e *env) seedBatch(s batchSpec) *models.Batch {
	b := models.Batch{
		Name:          "测试批次",
		DiscountType:  s.typ,
		DiscountValue: s.value,
		MinAmount:     s.min,
		MemberLevels:  s.members,
		ProductIDs:    s.pids,
		ValidFrom:     s.from,
		ValidUntil:    s.until,
	}
	if err := e.db.Create(&b).Error; err != nil {
		panic(err)
	}
	return &b
}

func (e *env) seedCode(batchID uint, full string) *models.Code {
	c := models.Code{BatchID: batchID, FullCode: full, Status: models.CodeStatusActive}
	if err := e.db.Create(&c).Error; err != nil {
		panic(err)
	}
	return &c
}

func items(pid uint, qty int) []app.QuoteItemInput {
	return []app.QuoteItemInput{{ProductID: pid, Qty: qty}}
}

var wideWindow = batchSpec{
	typ: "percent", value: 20, min: 2000,
	from:  time.Now().Add(-time.Hour),
	until: time.Now().Add(time.Hour),
}

// ---------- 核价边界 ----------

func TestPricingBoundaries(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("拿铁", 3000)
	now := time.Now()

	cases := []struct {
		name   string
		spec   batchSpec
		member string
		code   bool
		when   time.Time
		order  []app.QuoteItemInput
		wantOK bool
		reason string
	}{
		{"正常生效", wideWindow, "normal", true, now, items(pid, 1), true, pricing.ReasonOK},
		{"未到生效时间", wideWindow, "normal", true, now.Add(-2 * time.Hour), items(pid, 1), false, pricing.ReasonNotStarted},
		{"刚过期1纳秒", wideWindow, "normal", true, now.Add(2 * time.Hour), items(pid, 1), false, pricing.ReasonExpired},
		{"会员等级不符", batchSpec{typ: "fixed", value: 1500, min: 0, members: []string{"gold"}, from: now.Add(-time.Hour), until: now.Add(time.Hour)}, "normal", true, now, items(pid, 1), false, pricing.ReasonMemberNotAllowed},
		{"无适用商品", batchSpec{typ: "percent", value: 20, pids: []uint{999}, from: now.Add(-time.Hour), until: now.Add(time.Hour)}, "normal", true, now, items(pid, 1), false, pricing.ReasonNoEligibleItem},
		{"低于门槛1分", batchSpec{typ: "percent", value: 20, min: 3001, from: now.Add(-time.Hour), until: now.Add(time.Hour)}, "normal", true, now, items(pid, 1), false, pricing.ReasonBelowMinAmount},
		{"恰达门槛", batchSpec{typ: "percent", value: 20, min: 3000, from: now.Add(-time.Hour), until: now.Add(time.Hour)}, "normal", true, now, items(pid, 1), true, pricing.ReasonOK},
		{"无码原价", wideWindow, "normal", false, now, items(pid, 1), false, "NO_CODE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := e.seedBatch(tc.spec)
			in := app.QuoteInput{MemberLevel: tc.member, Items: tc.order}
			if tc.code {
				c := e.seedCode(b.ID, "CODE-"+tc.name)
				in.Code = c.FullCode
			}
			o, err := e.svc.CreateOrder(in, tc.when)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantOK {
				if o.DiscountAmount != 600 || o.FinalAmount != 2400 {
					t.Fatalf("期望折扣600/应付2400, 实得 %d/%d", o.DiscountAmount, o.FinalAmount)
				}
				if o.AppliedRule == nil || o.AppliedRule.CodeTail != models.Tail(in.Code) {
					t.Fatalf("缺少规则快照或尾号不对: %+v", o.AppliedRule)
				}
			} else {
				if o.DiscountAmount != 0 || o.FinalAmount != o.OriginalAmount {
					t.Fatalf("期望无折扣, 实得折扣 %d", o.DiscountAmount)
				}
				if o.RejectReason != tc.reason {
					t.Fatalf("原因码 = %s, 期望 %s", o.RejectReason, tc.reason)
				}
			}
		})
	}
}

// 有效期临界：ValidUntil 当刻仍可用，过 1ns 失效
func TestValidityExactBoundary(t *testing.T) {
	pidNow := time.Now()
	_ = pidNow
	b := models.Batch{
		Name: "边界", DiscountType: "percent", DiscountValue: 20, MinAmount: 0,
		ValidFrom: time.Unix(1000, 0), ValidUntil: time.Unix(2000, 0),
	}
	its := []models.OrderItem{{ProductID: 1, Name: "x", UnitPrice: 10000, Qty: 1, LineTotal: 10000}}
	c := &models.Code{ID: 1, FullCode: "EDGE-0001", Status: "active"}

	qAt := pricing.Evaluate(its, "normal", c, &b, time.Unix(2000, 0))
	if qAt.DiscountAmount != 2000 {
		t.Fatalf("有效期当刻应生效, discount=%d reason=%s", qAt.DiscountAmount, qAt.Reason)
	}
	qAfter := pricing.Evaluate(its, "normal", c, &b, time.Unix(2000, 1))
	if qAfter.DiscountAmount != 0 || qAfter.Reason != pricing.ReasonExpired {
		t.Fatalf("过期1ns应失效, discount=%d reason=%s", qAfter.DiscountAmount, qAfter.Reason)
	}
}

// ---------- 封禁前后的核价边界 ----------

func TestBanPendingStripsDiscount_PaidKeepsSnapshot(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("拿铁", 3000)
	now := time.Now()
	b := e.seedBatch(wideWindow)
	c1 := e.seedCode(b.ID, "PENDING-0001")
	c2 := e.seedCode(b.ID, "PAID000-0002")

	// 订单 A：待付款
	oa, err := e.svc.CreateOrder(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1), Code: c1.FullCode}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 订单 B：先付款成功
	ob, err := e.svc.CreateOrder(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1), Code: c2.FullCode}, now)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := e.svc.Pay(ob.ID, app.PayInput{ReceiptNo: "R-B"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if pb.Order.FinalAmount != 2400 || pb.Payment.DiscountAmount != 600 {
		t.Fatalf("订单B支付金额不对: %+v", pb)
	}

	// 封禁批次
	res, err := e.svc.BanBatch(b.ID, "泄露", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res.BannedCodes != 2 || res.RequotedOrders != 1 || res.AffectedPaid != 1 {
		t.Fatalf("封禁统计不对: %+v", res)
	}

	// 待付款订单 A 被重新核价：折扣剥离
	var gotA models.Order
	e.db.First(&gotA, oa.ID)
	if gotA.DiscountAmount != 0 || gotA.FinalAmount != 3000 || gotA.AppliedRule != nil {
		t.Fatalf("待付款订单应剥离折扣: %+v", gotA)
	}
	var hist []models.PriceHistory
	e.db.Where("order_id = ?", oa.ID).Order("id").Find(&hist)
	if len(hist) != 2 || hist[1].Reason != models.ReasonBanStripped || hist[1].DiscountAmount != 0 {
		t.Fatalf("价格历史应追加一条 BAN_STRIPPED: %+v", hist)
	}
	// 封禁后再核价同样无折扣
	q, err := e.svc.Quote(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1), Code: c1.FullCode}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if q.DiscountAmount != 0 || q.Reason != pricing.ReasonBatchBanned {
		t.Fatalf("封禁后核价应返回 BATCH_BANNED, got reason=%s", q.Reason)
	}

	// 已付款订单 B：金额与快照原样保留
	var gotB models.Order
	e.db.First(&gotB, ob.ID)
	if gotB.DiscountAmount != 600 || gotB.FinalAmount != 2400 || gotB.AppliedRule == nil {
		t.Fatalf("已付款订单优惠必须保留: %+v", gotB)
	}
	var payB models.Payment
	e.db.Where("order_id = ?", ob.ID).First(&payB)
	if payB.PaidAmount != 2400 || payB.AppliedRule == nil || payB.AppliedRule.CodeTail != "0002" {
		t.Fatalf("支付回执快照丢失: %+v", payB)
	}

	// A 封禁后支付：按原价，且不核销码
	pa, err := e.svc.Pay(oa.ID, app.PayInput{ReceiptNo: "R-A"}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Order.FinalAmount != 3000 || pa.Payment.CodeID != nil || pa.Payment.DiscountAmount != 0 {
		t.Fatalf("封禁后支付应原价且无核销: %+v", pa)
	}

	// 影响清单：只有 B（真实使用优惠并已付款），A 不在
	impact, err := e.svc.ImpactList(b.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(impact) != 1 || impact[0].Order.ID != ob.ID {
		var ids []uint
		for _, x := range impact {
			ids = append(ids, x.Order.ID)
		}
		t.Fatalf("影响清单应只含订单B, got %v", ids)
	}
	ent := impact[0]
	if ent.Payment == nil || ent.Payment.AppliedRule == nil ||
		ent.Order.AppliedRule.DiscountAmount != 600 || ent.Order.FinalAmount != 2400 {
		t.Fatalf("影响清单追不到原优惠快照: %+v", ent)
	}
	if ent.BatchName != "测试批次" || ent.BannedScope != "batch" {
		t.Fatalf("影响条目元数据不对: %+v", ent)
	}
}

// ---------- 并发：同码核销只成功一次 ----------

func TestConcurrentRedeemSameCode_OnlyOneSucceeds(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("拿铁", 3000)
	b := e.seedBatch(wideWindow)
	c := e.seedCode(b.ID, "CONCUR-0001")

	const n = 30
	var orderIDs []uint
	for i := 0; i < n; i++ {
		o, err := e.svc.CreateOrder(app.QuoteInput{
			MemberLevel: "normal", Items: items(pid, 1), Code: c.FullCode,
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		orderIDs = append(orderIDs, o.ID)
	}

	var ready, start sync.WaitGroup
	ready.Add(n)
	start.Add(1)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, oid := range orderIDs {
		wg.Add(1)
		go func(i int, oid uint) {
			defer wg.Done()
			ready.Done()
			start.Wait()
			_, errs[i] = e.svc.Pay(oid, app.PayInput{}, time.Now())
		}(i, oid)
	}
	ready.Wait()
	start.Done()
	wg.Wait()

	success, rejected := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, app.ErrCodeAlreadyRedeemed):
			rejected++
		default:
			t.Fatalf("出现非预期错误: %v", err)
		}
	}
	if success != 1 || rejected != n-1 {
		t.Fatalf("成功 %d 笔, 拒绝 %d 笔, 期望 1/%d", success, rejected, n-1)
	}
	var payN int64
	e.db.Model(&models.Payment{}).Where("code_id = ?", c.ID).Count(&payN)
	if payN != 1 {
		t.Fatalf("带该码的支付回执应有1条, got %d", payN)
	}
	var gotCode models.Code
	e.db.First(&gotCode, c.ID)
	if gotCode.Status != models.CodeStatusRedeemed || gotCode.RedeemedAt == nil {
		t.Fatalf("码状态应为 redeemed: %+v", gotCode)
	}
	// 每笔订单最多一条回执，金额自洽
	var payCount int64
	e.db.Model(&models.Payment{}).Count(&payCount)
	if payCount != 1 {
		t.Fatalf("总回执数应为1, got %d", payCount)
	}
}

// ---------- 并发：支付与封禁同时发生，结果由服务端核销顺序唯一确定 ----------

func TestPayVersusBanRace(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("拿铁", 3000)
	const rounds = 40
	var paidWithDiscount, paidFull int
	for i := 0; i < rounds; i++ {
		b := e.seedBatch(batchSpec{
			typ: "percent", value: 20, min: 2000,
			from: time.Now().Add(-time.Hour), until: time.Now().Add(time.Hour),
		})
		b.Name = fmt.Sprintf("轮次%d", i)
		e.db.Model(&models.Batch{}).Where("id = ?", b.ID).Update("name", b.Name)
		c := e.seedCode(b.ID, fmt.Sprintf("RACE-%04d-0001", i))
		o, err := e.svc.CreateOrder(app.QuoteInput{
			MemberLevel: "normal", Items: items(pid, 1), Code: c.FullCode,
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}

		var start sync.WaitGroup
		start.Add(1)
		var wg sync.WaitGroup
		var payErr, banErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			start.Wait()
			if i%2 == 0 {
				time.Sleep(2 * time.Millisecond) // 偶数轮：封禁先进入
			}
			_, payErr = e.svc.Pay(o.ID, app.PayInput{ReceiptNo: fmt.Sprintf("RACE-RCP-%04d", i)}, time.Now())
		}()
		go func() {
			defer wg.Done()
			start.Wait()
			if i%2 == 1 {
				time.Sleep(2 * time.Millisecond) // 奇数轮：支付先进入
			}
			_, banErr = e.svc.BanBatch(b.ID, "race", time.Now())
		}()
		start.Done()
		wg.Wait()
		if banErr != nil {
			t.Fatal(banErr)
		}

		var oo models.Order
		e.db.First(&oo, o.ID)
		var pays []models.Payment
		e.db.Where("order_id = ?", o.ID).Find(&pays)
		if len(pays) != 1 {
			t.Fatalf("第%d轮: 每笔订单只能有一条回执, got %d (payErr=%v)", i, len(pays), payErr)
		}
		p := pays[0]
		// 一笔订单只能有一个折扣金额：订单与回执的金额必须完全一致
		if oo.DiscountAmount != p.DiscountAmount || oo.FinalAmount != p.PaidAmount ||
			oo.OriginalAmount-oo.DiscountAmount != oo.FinalAmount {
			t.Fatalf("第%d轮: 金额不自洽 order=%+v payment=%+v", i, oo, p)
		}
		switch {
		case payErr == nil && p.DiscountAmount == 600 && p.CodeID != nil:
			paidWithDiscount++ // 支付先核销成功
		case payErr == nil && p.DiscountAmount == 0 && p.CodeID == nil:
			paidFull++ // 封禁先生效，折扣剥离
		default:
			t.Fatalf("第%d轮: 出现两种结果之外的状态 payErr=%v order=%+v pay=%+v", i, payErr, oo, p)
		}
	}
	if paidWithDiscount == 0 || paidFull == 0 {
		t.Fatalf("两种结果都应出现: 优惠成交 %d, 原价成交 %d", paidWithDiscount, paidFull)
	}
	t.Logf("40 轮竞争: 优惠成交 %d 笔, 原价成交 %d 笔（均无双重折扣/重复扣款）", paidWithDiscount, paidFull)
}

// ---------- 重复支付回执幂等 ----------

func TestDuplicateReceiptIdempotent(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("拿铁", 3000)
	b := e.seedBatch(wideWindow)
	c := e.seedCode(b.ID, "IDEM-0000-0001")
	o, err := e.svc.CreateOrder(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1), Code: c.FullCode}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	r1, err := e.svc.Pay(o.ID, app.PayInput{ReceiptNo: "DUP-RCP-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r1.Idempotent {
		t.Fatal("首次支付不应是重放")
	}
	// 同一回执重复到达
	r2, err := e.svc.Pay(o.ID, app.PayInput{ReceiptNo: "DUP-RCP-1"}, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Idempotent {
		t.Fatal("重复回执必须标记为幂等重放")
	}
	if r1.Payment.ID != r2.Payment.ID || r2.Payment.PaidAmount != 2400 || r2.Payment.DiscountAmount != 600 {
		t.Fatalf("重放必须返回同一条原始回执: %+v vs %+v", r1.Payment, r2.Payment)
	}
	var n int64
	e.db.Model(&models.Payment{}).Where("receipt_no = ?", "DUP-RCP-1").Count(&n)
	if n != 1 {
		t.Fatalf("回执只能落一条, got %d", n)
	}
	// 不带回执号再次通知已付款订单：同样幂等
	r3, err := e.svc.Pay(o.ID, app.PayInput{}, time.Now().Add(2*time.Second))
	if err != nil || !r3.Idempotent {
		t.Fatalf("已付款订单重复通知应幂等: %v %+v", err, r3)
	}
	e.db.Model(&models.Payment{}).Where("order_id = ?", o.ID).Count(&n)
	if n != 1 {
		t.Fatalf("订单回执仍只能有一条, got %d", n)
	}

	// 同一回执号用在另一笔订单上：拒绝
	o2, err := e.svc.CreateOrder(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Pay(o2.ID, app.PayInput{ReceiptNo: "DUP-RCP-1"}, time.Now()); !errors.Is(err, app.ErrReceiptConflict) {
		t.Fatalf("回执串单应报冲突, got %v", err)
	}

	// 金额不一致：拒绝收款
	o3, err := e.svc.CreateOrder(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bad := int64(9999)
	if _, err := e.svc.Pay(o3.ID, app.PayInput{ReceiptNo: "RCP-BAD", PaidAmount: &bad}, time.Now()); !errors.Is(err, app.ErrAmountMismatch) {
		t.Fatalf("金额不符应拒绝, got %v", err)
	}
	var o3got models.Order
	e.db.First(&o3got, o3.ID)
	if o3got.Status != models.OrderStatusPending {
		t.Fatalf("失败的支付不得改变订单状态: %+v", o3got)
	}
}

// ---------- 误封恢复规则 ----------

func TestRestoreRules(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("拿铁", 3000)
	now := time.Now()
	bValid := e.seedBatch(wideWindow)
	bExpired := e.seedBatch(batchSpec{
		typ: "percent", value: 20,
		from: now.Add(-2 * time.Hour), until: now.Add(-time.Hour),
	})

	// 未使用 + 有效：可以恢复
	c1 := e.seedCode(bValid.ID, "REST-0000-0001")
	if _, err := e.svc.BanCode(c1.ID, "误封", now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RestoreCode(c1.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("未使用有效码应可恢复: %v", err)
	}
	var gc1 models.Code
	e.db.First(&gc1, c1.ID)
	if gc1.BannedAt != nil || gc1.Status != models.CodeStatusActive {
		t.Fatalf("恢复后状态不对: %+v", gc1)
	}

	// 已核销：不可恢复
	c2 := e.seedCode(bValid.ID, "USED-0000-0002")
	o, err := e.svc.CreateOrder(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1), Code: c2.FullCode}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Pay(o.ID, app.PayInput{ReceiptNo: "R-REST"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BanCode(c2.ID, "事后封禁", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RestoreCode(c2.ID, now.Add(2*time.Second)); !errors.Is(err, app.ErrRestoreRule) {
		t.Fatalf("已核销码禁止重新开放, got %v", err)
	}
	var gc2 models.Code
	e.db.First(&gc2, c2.ID)
	if gc2.Status != models.CodeStatusRedeemed {
		t.Fatalf("已核销码恢复失败后状态不得变化: %+v", gc2)
	}
	// 该订单仍能在影响清单追溯
	imp, err := e.svc.ImpactList(0, c2.ID)
	if err != nil || len(imp) != 1 {
		t.Fatalf("已核销后封禁的订单应在影响清单: %v %d", err, len(imp))
	}

	// 过期批次的码：不可恢复
	c3 := e.seedCode(bExpired.ID, "EXPR-0000-0003")
	if _, err := e.svc.BanCode(c3.ID, "误封", now); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RestoreCode(c3.ID, now); !errors.Is(err, app.ErrRestoreRule) {
		t.Fatalf("过期码不可恢复, got %v", err)
	}

	// 批次解除封禁不会重新开放已核销码
	n, err := e.svc.RestoreBatch(bValid.ID, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	e.db.First(&gc2, c2.ID)
	if gc2.Status != models.CodeStatusRedeemed {
		t.Fatalf("批次恢复不得重开已核销码: %+v", gc2)
	}
	e.db.First(&gc1, c1.ID)
	if gc1.BannedAt != nil {
		t.Fatalf("未核销码应随批次恢复: %+v (restored=%d)", gc1, n)
	}
}

// ---------- 列表只显示尾号，完整码不外泄 ----------

func TestCodeListMasksFullCode(t *testing.T) {
	e := setup(t)
	_ = e.seedProduct("拿铁", 3000)
	b := e.seedBatch(wideWindow)
	c := e.seedCode(b.ID, "SECRET-ABCD-9988")
	views, err := e.svc.ListCodes(0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(views)
	if stringContains(string(raw), "SECRET-ABCD-9988") {
		t.Fatalf("列表数据中泄露完整码: %s", raw)
	}
	var found bool
	for _, v := range views {
		if v.ID == c.ID {
			found = true
			if v.Tail != "****9988" {
				t.Fatalf("尾号展示不对: %s", v.Tail)
			}
		}
	}
	if !found {
		t.Fatal("列表中未找到该码")
	}
}

// fixed 折扣不超过适用金额
func TestFixedDiscountCapped(t *testing.T) {
	e := setup(t)
	pid := e.seedProduct("小件", 1000)
	now := time.Now()
	b := e.seedBatch(batchSpec{typ: "fixed", value: 5000, from: now.Add(-time.Hour), until: now.Add(time.Hour)})
	c := e.seedCode(b.ID, "CAP-0000-0001")
	q, err := e.svc.Quote(app.QuoteInput{MemberLevel: "normal", Items: items(pid, 1), Code: c.FullCode}, now)
	if err != nil {
		t.Fatal(err)
	}
	if q.DiscountAmount != 1000 || q.FinalAmount != 0 {
		t.Fatalf("fixed 折扣应封顶为适用金额, got d=%d f=%d", q.DiscountAmount, q.FinalAmount)
	}
}

func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
