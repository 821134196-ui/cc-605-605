package pricing

import (
	"testing"
	"time"

	"promoleak/internal/models"
)

func testBatch() *models.PromoBatch {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return &models.PromoBatch{
		ID: 1, ApplicableTo: "gold,silver", ProductIDs: "*",
		ValidFrom: t0, ValidUntil: t0.Add(24 * time.Hour),
		DiscountType: "percent", DiscountValue: 20,
	}
}

func testCode() *models.PromoCode { return &models.PromoCode{ID: 1, BatchID: 1, Code: "ABCDEFGH1234"} }

func ctxAt(t time.Time) Context {
	return Context{
		MemberLevel: "gold",
		At:          t,
		Items: []models.Item{
			{ProductID: 1, PriceFen: 10000, Qty: 1, LineTotalFen: 10000},
		},
	}
}

func TestEvaluate_HappyPath(t *testing.T) {
	b, c := testBatch(), testCode()
	at := b.ValidFrom.Add(time.Hour)
	reason, disc := Evaluate(b, c, ctxAt(at))
	if reason != OK || disc != 2000 {
		t.Fatalf("want ok/2000, got %v/%d", reason, disc)
	}
}

// 封禁前后的核价边界。
func TestEvaluate_BanAndValidityBoundaries(t *testing.T) {
	b, c := testBatch(), testCode()

	// 恰好生效时刻可用
	if r, _ := Evaluate(b, c, ctxAt(b.ValidFrom)); r != OK {
		t.Fatalf("at ValidFrom should be OK, got %v", r)
	}
	// 恰好失效时刻不可用
	if r, _ := Evaluate(b, c, ctxAt(b.ValidUntil)); r != ErrExpired {
		t.Fatalf("at ValidUntil want expired, got %v", r)
	}
	// 未生效
	if r, _ := Evaluate(b, c, ctxAt(b.ValidFrom.Add(-time.Second))); r != ErrNotStarted {
		t.Fatalf("before ValidFrom want not_started, got %v", r)
	}
	// 封禁：哪怕在有效期正中间，也必须拒绝
	banned := b.ValidFrom.Add(time.Hour)
	c2 := *c
	c2.BannedAt = &banned
	if r, disc := Evaluate(b, &c2, ctxAt(banned.Add(time.Minute))); r != ErrBanned || disc != 0 {
		t.Fatalf("banned code want ErrBanned/0, got %v/%d", r, disc)
	}
	// 已核销：不可再用于第二单
	rt := b.ValidFrom.Add(time.Hour)
	c3 := *c
	c3.RedeemedAt = &rt
	if r, _ := Evaluate(b, &c3, ctxAt(rt.Add(time.Minute))); r != ErrRedeemed {
		t.Fatalf("redeemed code want ErrRedeemed, got %v", r)
	}
}

func TestEvaluate_MemberAndProductScope(t *testing.T) {
	b, c := testBatch(), testCode()
	at := b.ValidFrom.Add(time.Hour)
	ctx := ctxAt(at)

	ctx.MemberLevel = "normal"
	if r, _ := Evaluate(b, c, ctx); r != ErrMemberMismatch {
		t.Fatalf("normal member want mismatch, got %v", r)
	}
	ctx.MemberLevel = "gold"

	// 限定商品范围：购物篮里只有不适用商品 → 拒绝
	b2 := *b
	b2.ProductIDs = "99"
	ctx.Items = []models.Item{{ProductID: 1, LineTotalFen: 10000, Qty: 1}}
	if r, _ := Evaluate(&b2, c, ctx); r != ErrEmptyBasket {
		t.Fatalf("non-eligible basket want empty, got %v", r)
	}

	// 部分适用：折扣只算适用商品
	b2.ProductIDs = "1"
	ctx.Items = []models.Item{
		{ProductID: 1, LineTotalFen: 5000, Qty: 1},
		{ProductID: 2, LineTotalFen: 5000, Qty: 1},
	}
	if _, disc := Evaluate(&b2, c, ctx); disc != 1000 {
		t.Fatalf("discount should only apply to eligible goods, got %d", disc)
	}
}

func TestEvaluate_DiscountCaps(t *testing.T) {
	b, c := testBatch(), testCode()
	at := b.ValidFrom.Add(time.Hour)
	ctx := ctxAt(at)

	// fixed 立减大于商品总额时封顶
	b.DiscountType = "fixed"
	b.DiscountValue = 20000
	ctx.Items[0].LineTotalFen = 10000
	if _, disc := Evaluate(b, c, ctx); disc != 10000 {
		t.Fatalf("fixed discount capped at goods total, got %d", disc)
	}
}

func TestRecomputeAfterBan(t *testing.T) {
	o := &models.Order{
		Status: "pending", GoodsTotalFen: 10000, DiscountFen: 2000, PayFen: 8000,
		Promo: &models.AppliedPromo{DiscountFen: 2000}, PromoCodeID: ptr(uint(1)),
	}
	RecomputeAfterBan(o)
	if o.DiscountFen != 0 || o.PayFen != 10000 || o.Promo != nil || o.PromoCodeID != nil {
		t.Fatalf("pending order must be fully repriced: %+v", o)
	}
	if o.RepricedCount != 1 {
		t.Fatalf("repriced count = %d", o.RepricedCount)
	}

	// 已付款单不能被重核价
	paid := &models.Order{Status: "paid", GoodsTotalFen: 10000, DiscountFen: 2000, PayFen: 8000}
	RecomputeAfterBan(paid)
	if paid.DiscountFen != 2000 || paid.PayFen != 8000 {
		t.Fatalf("paid order snapshot must be preserved: %+v", paid)
	}
}

func TestMaskTail(t *testing.T) {
	if got := MaskTail("NEWBIE-5K2L-0001"); got != "************0001" {
		t.Fatalf("mask = %q", got)
	}
}

func ptr[T any](v T) *T { return &v }
