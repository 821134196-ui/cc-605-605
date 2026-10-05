package pricing

import (
	"time"

	"discountguard/internal/models"
)

// 未生效原因码
const (
	ReasonOK               = "OK"
	ReasonCodeNotFound     = "CODE_NOT_FOUND"
	ReasonCodeBanned       = "CODE_BANNED"
	ReasonCodeRedeemed     = "CODE_REDEEMED"
	ReasonBatchBanned      = "BATCH_BANNED"
	ReasonExpired          = "EXPIRED"
	ReasonNotStarted       = "NOT_STARTED"
	ReasonMemberNotAllowed = "MEMBER_NOT_ALLOWED"
	ReasonNoEligibleItem   = "NO_ELIGIBLE_ITEM"
	ReasonBelowMinAmount   = "BELOW_MIN_AMOUNT"
)

// Quote 一次核价结果
type Quote struct {
	OriginalAmount int64                `json:"original_amount"`
	EligibleAmount int64                `json:"eligible_amount"`
	DiscountAmount int64                `json:"discount_amount"`
	FinalAmount    int64                `json:"final_amount"`
	Reason         string               `json:"reason"`
	Rule           *models.RuleSnapshot `json:"rule,omitempty"`
}

func contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Evaluate 纯核价逻辑，不访问数据库、不取系统时间（时间由入参 now 注入，便于边界测试）。
// code 为 nil 表示不使用折扣码；banned/redeemed/batch 状态由入参携带。
func Evaluate(items []models.OrderItem, memberLevel string, code *models.Code, batch *models.Batch, now time.Time) Quote {
	var original int64
	for _, it := range items {
		original += it.LineTotal
	}
	base := Quote{OriginalAmount: original, EligibleAmount: 0, DiscountAmount: 0, FinalAmount: original, Reason: ReasonOK}

	if code == nil {
		base.Reason = "NO_CODE"
		return base
	}
	if batch == nil {
		base.Reason = ReasonCodeNotFound
		return base
	}
	switch {
	case code.BannedAt != nil:
		base.Reason = ReasonCodeBanned
	case batch.BannedAt != nil:
		base.Reason = ReasonBatchBanned
	case code.Status == models.CodeStatusRedeemed:
		base.Reason = ReasonCodeRedeemed
	case now.Before(batch.ValidFrom):
		base.Reason = ReasonNotStarted
	case now.After(batch.ValidUntil):
		base.Reason = ReasonExpired
	case len(batch.MemberLevels) > 0 && !contains(batch.MemberLevels, memberLevel):
		base.Reason = ReasonMemberNotAllowed
	}
	if base.Reason != ReasonOK {
		return base
	}

	allowed := map[uint]bool{}
	for _, pid := range batch.ProductIDs {
		allowed[pid] = true
	}
	var eligible int64
	for _, it := range items {
		if len(allowed) == 0 || allowed[it.ProductID] {
			eligible += it.LineTotal
		}
	}
	base.EligibleAmount = eligible
	if eligible == 0 {
		base.Reason = ReasonNoEligibleItem
		return base
	}
	if batch.MinAmount > 0 && eligible < batch.MinAmount {
		base.Reason = ReasonBelowMinAmount
		return base
	}

	var discount int64
	switch batch.DiscountType {
	case "percent":
		discount = eligible * batch.DiscountValue / 100
	case "fixed":
		discount = batch.DiscountValue
	}
	if discount > eligible {
		discount = eligible // 折扣不超过适用商品金额，订单不会变成负数
	}

	base.DiscountAmount = discount
	base.FinalAmount = original - discount
	base.Rule = &models.RuleSnapshot{
		BatchID:        batch.ID,
		BatchName:      batch.Name,
		CodeID:         code.ID,
		CodeTail:       models.Tail(code.FullCode),
		DiscountType:   batch.DiscountType,
		DiscountValue:  batch.DiscountValue,
		MinAmount:      batch.MinAmount,
		MemberLevels:   append([]string(nil), batch.MemberLevels...),
		ProductIDs:     append([]uint(nil), batch.ProductIDs...),
		EligibleAmount: eligible,
		OriginalAmount: original,
		DiscountAmount: discount,
		FinalAmount:    original - discount,
		EvaluatedAt:    now,
	}
	return base
}
