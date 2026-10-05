// Package pricing 负责折扣码的适用性校验与金额计算。纯函数，不碰数据库，
// 方便对“封禁前后的核价边界”做精确测试。
package pricing

import (
	"fmt"
	"strings"
	"time"

	"promoleak/internal/models"
)

type RejectReason string

const (
	OK                RejectReason = ""
	ErrNotFound       RejectReason = "code_not_found"
	ErrBanned         RejectReason = "code_banned"
	ErrRedeemed       RejectReason = "code_redeemed"
	ErrNotStarted     RejectReason = "code_not_started"
	ErrExpired        RejectReason = "code_expired"
	ErrMemberMismatch RejectReason = "member_not_applicable"
	ErrEmptyBasket    RejectReason = "empty_basket"
)

// Context 一次核价的输入。at 为服务端核价时刻（封禁/支付竞态时由调用方传服务端时间）。
type Context struct {
	MemberLevel string
	Items       []models.Item
	At          time.Time
}

// Evaluate 在给定批次与码状态下计算优惠。返回拒绝原因与折扣金额（分）。
func Evaluate(batch *models.PromoBatch, code *models.PromoCode, c Context) (RejectReason, int64) {
	if code == nil || batch == nil {
		return ErrNotFound, 0
	}
	if code.BannedAt != nil {
		return ErrBanned, 0
	}
	if code.RedeemedAt != nil {
		return ErrRedeemed, 0
	}
	// 有效期为半开区间 [ValidFrom, ValidUntil)：到达 ValidUntil 即刻失效。
	if c.At.Before(batch.ValidFrom) {
		return ErrNotStarted, 0
	}
	if !c.At.Before(batch.ValidUntil) {
		return ErrExpired, 0
	}
	if !memberAllowed(batch.ApplicableTo, c.MemberLevel) {
		return ErrMemberMismatch, 0
	}

	eligible := eligibleGoods(batch.ProductIDs, c.Items)
	var eligibleTotal int64
	for _, it := range eligible {
		eligibleTotal += it.LineTotalFen
	}
	if len(c.Items) == 0 || eligibleTotal <= 0 {
		return ErrEmptyBasket, 0
	}

	disc := computeDiscount(batch, eligibleTotal)
	if disc > eligibleTotal {
		disc = eligibleTotal // 优惠不超过适用商品金额
	}
	return OK, disc
}

// RecomputeAfterBan 封禁后对待付款订单重新核价：码一旦封禁，优惠必须清零，
// 应付恢复为商品总额。已付款订单不会走到这里。
func RecomputeAfterBan(order *models.Order) {
	if order.Status != "pending" {
		return
	}
	order.Promo = nil
	order.PromoCodeID = nil
	order.DiscountFen = 0
	order.PayFen = order.GoodsTotalFen
	order.RepricedCount++
}

func memberAllowed(spec, level string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "*" || spec == "" {
		return true
	}
	for _, l := range strings.Split(spec, ",") {
		if strings.TrimSpace(l) == level {
			return true
		}
	}
	return false
}

func eligibleGoods(spec string, items []models.Item) []models.Item {
	spec = strings.TrimSpace(spec)
	if spec == "*" || spec == "" {
		return items
	}
	allowed := map[uint]bool{}
	for _, s := range strings.Split(spec, ",") {
		var id uint
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &id); err == nil && id > 0 {
			allowed[id] = true
		}
	}
	var out []models.Item
	for _, it := range items {
		if allowed[it.ProductID] {
			out = append(out, it)
		}
	}
	return out
}

func computeDiscount(batch *models.PromoBatch, eligibleTotal int64) int64 {
	switch batch.DiscountType {
	case "percent":
		// 四舍五入到分。
		return (eligibleTotal*batch.DiscountValue + 50) / 100
	case "fixed":
		if batch.DiscountValue > eligibleTotal {
			return eligibleTotal
		}
		return batch.DiscountValue
	default:
		return 0
	}
}

// MaskTail 只保留码尾 4 位，其余打码。
func MaskTail(code string) string {
	r := []rune(code)
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-4) + string(r[len(r)-4:])
}
