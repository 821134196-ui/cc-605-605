package service

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"promoleak/internal/models"
	"promoleak/internal/pricing"
	"promoleak/internal/store"
)

var testDBCounter uint64

func newTestSvc(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	// 每次调用独立内存库（-count=N 时名字相同，需自带序号）。
	n := atomic.AddUint64(&testDBCounter, 1)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") +
		fmt.Sprintf("_%d?mode=memory&cache=shared", n)
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Seed(db); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return New(db), db
}

const annivCode = "ANNIV-8X4K-29FD-LQ17" // 批次1：8折，gold/silver，全商品
const newbieCode = "NEWBIE-5K2L-0001"    // 批次2：立减10元，全员，商品1/2/4

func basket() []ItemInput {
	return []ItemInput{{ProductID: 1, Qty: 1}, {ProductID: 2, Qty: 1}} // 3200+1800=5000
}

func mustCreate(t *testing.T, svc *Service, memberID uint, code string) *models.Order {
	t.Helper()
	o, reason, err := svc.CreateOrder(CreateOrderInput{MemberID: memberID, Items: basket(), Code: code})
	if err != nil || reason != pricing.OK {
		t.Fatalf("create order: err=%v reason=%v", err, reason)
	}
	return o
}

// 封禁前：待付款单带 8 折优惠（5000-1000）。封禁后：重新核价，折扣清零、应付恢复原价。
func TestBan_RepricesPendingOrder(t *testing.T) {
	svc, _ := newTestSvc(t)
	o := mustCreate(t, svc, 1, annivCode) // 金卡
	if o.DiscountFen != 1000 || o.PayFen != 4000 {
		t.Fatalf("pre-ban quote: disc=%d pay=%d", o.DiscountFen, o.PayFen)
	}

	res, err := svc.BanCodes([]uint{1}, "截图泄露")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RepricedOrders) != 1 || res.RepricedOrders[0] != o.OrderNo {
		t.Fatalf("repriced = %v", res.RepricedOrders)
	}

	got, err := svc.Order(o.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.DiscountFen != 0 || got.PayFen != 5000 {
		t.Fatalf("post-ban pending order must be full price: %+v", got)
	}
	if got.Promo != nil || got.PromoCodeID != nil {
		t.Fatalf("promo must be detached after reprice: %+v", got)
	}
	if got.RepricedCount != 1 {
		t.Fatalf("repriced_count=%d", got.RepricedCount)
	}
	// 封禁后用同一码不能再下单
	if _, reason, err := svc.CreateOrder(CreateOrderInput{MemberID: 1, Items: basket(), Code: annivCode}); err != nil || reason != pricing.ErrBanned {
		t.Fatalf("banned code reuse: reason=%v err=%v", reason, err)
	}
}

// 已付款单封禁后保留当时确认的优惠，并进入影响清单可逐笔追溯。
func TestBan_KeepsPaidOrderAndAppearsInImpact(t *testing.T) {
	svc, _ := newTestSvc(t)
	o := mustCreate(t, svc, 1, annivCode)
	paid, idem, err := svc.Pay(o.OrderNo, "RC-BEFORE-BAN")
	if err != nil || idem {
		t.Fatalf("pay: %v idem=%v", err, idem)
	}
	if paid.DiscountFen != 1000 || paid.PayFen != 4000 || paid.RedeemedAt == nil {
		t.Fatalf("paid snapshot wrong: %+v", paid)
	}

	if _, err := svc.BanCodes([]uint{1}, "事后封禁"); err != nil {
		t.Fatal(err)
	}

	// 订单金额快照不变
	got, _ := svc.Order(o.OrderNo)
	if got.DiscountFen != 1000 || got.PayFen != 4000 || got.Promo == nil || got.Promo.DiscountFen != 1000 {
		t.Fatalf("paid order promo snapshot must survive ban: %+v", got)
	}
	if got.Promo.CodeTail != "****************LQ17" {
		t.Fatalf("snapshot should keep masked tail only: %s", got.Promo.CodeTail)
	}

	rows, err := svc.ImpactList()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("impact rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.OrderNo != o.OrderNo || r.CodeTail != "****************LQ17" || r.PayFen != 4000 {
		t.Fatalf("impact row wrong: %+v", r)
	}
	if r.Promo == nil || r.Promo.BatchName == "" || r.Promo.DiscountFen != 1000 {
		t.Fatalf("impact must carry original promo rule snapshot: %+v", r.Promo)
	}
	if !r.RedeemedAt.Before(*r.BannedAt) && !r.RedeemedAt.Equal(*r.BannedAt) {
		t.Fatalf("redeem must be at/before ban: redeem=%v ban=%v", r.RedeemedAt, r.BannedAt)
	}
}

// 同一码并发核销：只能成功一次；一单享折扣，另一单按原价；码只被核销一次。
func TestConcurrentRedeem_OnlyOneWins(t *testing.T) {
	svc, db := newTestSvc(t)
	// 两个都满足适用会员的单（金卡/银卡），挂同一个码
	o1 := mustCreate(t, svc, 1, annivCode)
	o2 := mustCreate(t, svc, 2, annivCode)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); <-start; _, _, e := svc.Pay(o1.OrderNo, "RC-A"); errs <- e }()
	go func() { defer wg.Done(); <-start; _, _, e := svc.Pay(o2.OrderNo, "RC-B"); errs <- e }()
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent pay error: %v", e)
		}
	}

	g1, _ := svc.Order(o1.OrderNo)
	g2, _ := svc.Order(o2.OrderNo)
	discWinners := 0
	for _, g := range []*models.Order{g1, g2} {
		if g.Status != "paid" {
			t.Fatalf("both orders must be paid, %s=%s", g.OrderNo, g.Status)
		}
		if g.DiscountFen == 1000 && g.PayFen == 4000 && g.Promo != nil && g.RedeemedAt != nil {
			discWinners++
		} else if g.DiscountFen != 0 || g.PayFen != 5000 || g.Promo != nil {
			t.Fatalf("losing order must be full price with no promo: %+v", g)
		}
	}
	if discWinners != 1 {
		t.Fatalf("exactly one order may redeem the code, winners=%d", discWinners)
	}

	var pc models.PromoCode
	if err := db.First(&pc, 1).Error; err != nil {
		t.Fatal(err)
	}
	if pc.RedeemedAt == nil || pc.OrderNo == nil {
		t.Fatalf("code must be redeemed once: %+v", pc)
	}
	var rcCount int64
	db.Model(&models.PaymentReceipt{}).Count(&rcCount)
	if rcCount != 2 {
		t.Fatalf("two distinct receipts expected, got %d", rcCount)
	}
}

// 重复支付回执：幂等返回，不重复扣款、不改金额、不重复核销。
func TestDuplicateReceipt_IsIdempotent(t *testing.T) {
	svc, db := newTestSvc(t)
	o := mustCreate(t, svc, 1, annivCode)

	first, idem1, err := svc.Pay(o.OrderNo, "RC-DUP")
	if err != nil || idem1 {
		t.Fatalf("first pay: %v idem=%v", err, idem1)
	}
	second, idem2, err := svc.Pay(o.OrderNo, "RC-DUP")
	if err != nil || !idem2 {
		t.Fatalf("duplicate receipt must be idempotent: err=%v idem=%v", err, idem2)
	}
	if first.DiscountFen != second.DiscountFen || first.PayFen != second.PayFen {
		t.Fatalf("amounts changed on replay: %+v vs %+v", first, second)
	}
	var n int64
	db.Model(&models.PaymentReceipt{}).Where("receipt_no = ?", "RC-DUP").Count(&n)
	if n != 1 {
		t.Fatalf("receipt rows = %d, want 1", n)
	}
}

// 回执号被另一笔订单复用：拒绝，不改变待付款单状态。
func TestReceiptReuseAcrossOrders_Rejected(t *testing.T) {
	svc, _ := newTestSvc(t)
	o1 := mustCreate(t, svc, 1, newbieCode)
	o2 := mustCreate(t, svc, 2, annivCode)
	if _, _, err := svc.Pay(o1.OrderNo, "RC-SHARED"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Pay(o2.OrderNo, "RC-SHARED"); err != ErrBadReceipt {
		t.Fatalf("want ErrBadReceipt, got %v", err)
	}
	got, _ := svc.Order(o2.OrderNo)
	if got.Status != "pending" {
		t.Fatalf("second order must stay pending, got %s", got.Status)
	}
}

// 支付与封禁同时发生：按服务端核销时间裁决——
// 先封后付 → 不享折扣；先付后封 → 保留折扣并入影响清单。
func TestPayVsBan_BoundaryByServerRedeemTime(t *testing.T) {
	svc, _ := newTestSvc(t)

	// 场景A：封禁先落库，支付后发生 → 原价
	oa := mustCreate(t, svc, 1, newbieCode)
	if _, err := svc.BanCodes([]uint{4}, "泄露"); err != nil {
		t.Fatal(err)
	}
	pa, _, err := svc.Pay(oa.OrderNo, "RC-AFTER-BAN")
	if err != nil {
		t.Fatal(err)
	}
	if pa.DiscountFen != 0 || pa.PayFen != 5000 || pa.Promo != nil || pa.RedeemedAt != nil {
		t.Fatalf("payment after ban must be full price: %+v", pa)
	}

	// 场景B：支付先核销，封禁后发生 → 快照保留
	ob := mustCreate(t, svc, 1, annivCode)
	pb, _, err := svc.Pay(ob.OrderNo, "RC-BEFORE")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BanCodes([]uint{1}, "泄露"); err != nil {
		t.Fatal(err)
	}
	gb, _ := svc.Order(ob.OrderNo)
	if gb.DiscountFen != 1000 || gb.PayFen != 4000 || gb.Promo == nil {
		t.Fatalf("payment before ban keeps promo: %+v", gb)
	}
	if pb.RedeemedAt == nil {
		t.Fatal("redeemed_at must be stamped")
	}
}

// 误封恢复：未核销且在有效期内可恢复；已核销的码不能重新开放；过期批次不可恢复。
func TestUnbanRules(t *testing.T) {
	svc, db := newTestSvc(t)

	// 未使用 + 有效期内 → 可恢复
	if _, err := svc.BanCodes([]uint{2}, "误封"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnbanCode(2); err != nil {
		t.Fatalf("unused valid code should be unbanable: %v", err)
	}
	var pc models.PromoCode
	db.First(&pc, 2)
	if pc.BannedAt != nil {
		t.Fatal("banned_at should be cleared")
	}
	// 恢复后可正常核销
	o := mustCreate(t, svc, 1, "ANNIV-7P2M-X8VW-TM63")
	if o.DiscountFen != 1000 {
		t.Fatalf("recovered code should apply, disc=%d", o.DiscountFen)
	}

	// 已核销 → 封禁后不可恢复
	o2 := mustCreate(t, svc, 1, annivCode)
	if _, _, err := svc.Pay(o2.OrderNo, "RC-X"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BanCodes([]uint{1}, "泄露"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnbanCode(1); err != ErrUnbanRedeemed {
		t.Fatalf("redeemed code must not reopen, got %v", err)
	}

	// 过期批次中的码不可恢复
	expired := models.PromoBatch{
		Name: "已过期批次", ApplicableTo: "*", ProductIDs: "*",
		ValidFrom: time.Now().Add(-72 * time.Hour), ValidUntil: time.Now().Add(-1 * time.Hour),
		DiscountType: "fixed", DiscountValue: 100,
	}
	if err := db.Create(&expired).Error; err != nil {
		t.Fatal(err)
	}
	old := models.PromoCode{BatchID: expired.ID, Code: "EXPIRED-CODE-0001"}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BanCodes([]uint{old.ID}, "误封"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UnbanCode(old.ID); err != ErrUnbanExpired {
		t.Fatalf("expired code unban must fail, got %v", err)
	}
}

// 列表只暴露尾号，任何对外结构中都不含完整明文码。
func TestMaskedList_NeverLeaksFullCode(t *testing.T) {
	svc, _ := newTestSvc(t)
	cs, err := svc.ListMaskedCodes(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if len(c.CodeTail) < 4 || c.CodeTail[len(c.CodeTail)-4:] == c.CodeTail {
			// 尾号字段本身不应等于明文
		}
		for _, ch := range c.CodeTail[:len(c.CodeTail)-4] {
			if ch != '*' {
				t.Fatalf("masked tail leaked chars: %q", c.CodeTail)
			}
		}
	}
}
