package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"promoleak/internal/models"
	"promoleak/internal/pricing"
)

// clock 便于测试精确控制“服务端核销时间”。
func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type CreateOrderInput struct {
	MemberID uint        `json:"member_id"`
	Items    []ItemInput `json:"items"`
	Code     string      `json:"code"` // 结账时输入的完整码
}

type ItemInput struct {
	ProductID uint `json:"product_id"`
	Qty       int  `json:"qty"`
}

type QuoteResult struct {
	Reason      pricing.RejectReason `json:"reason,omitempty"`
	DiscountFen int64                `json:"discount_fen"`
}

// PreviewQuote 只核价不下单，供前端输入码时即时反馈。
func (s *Service) PreviewQuote(memberID uint, items []ItemInput, code string) (*models.Order, QuoteResult, error) {
	o, reason, err := s.buildOrder(memberID, items, code)
	if err != nil {
		return nil, QuoteResult{}, err
	}
	return o, QuoteResult{Reason: reason, DiscountFen: o.DiscountFen}, nil
}

// CreateOrder 建待付款单；码不可用时按拒绝原因返回 400，不静默吞掉。
func (s *Service) CreateOrder(in CreateOrderInput) (*models.Order, pricing.RejectReason, error) {
	o, reason, err := s.buildOrder(in.MemberID, in.Items, in.Code)
	if err != nil {
		return nil, reason, err
	}
	if in.Code != "" && reason != pricing.OK {
		return nil, reason, nil
	}
	if err := s.DB.Create(o).Error; err != nil {
		return nil, pricing.OK, err
	}
	return o, pricing.OK, nil
}

func (s *Service) buildOrder(memberID uint, in []ItemInput, code string) (*models.Order, pricing.RejectReason, error) {
	if len(in) == 0 {
		return nil, pricing.OK, ErrEmptyBasket
	}
	var member models.Member
	if err := s.DB.First(&member, memberID).Error; err != nil {
		return nil, pricing.OK, ErrNotFound
	}
	var items []models.Item
	var goodsTotal int64
	for _, ii := range in {
		if ii.Qty <= 0 {
			continue
		}
		var p models.Product
		if err := s.DB.First(&p, ii.ProductID).Error; err != nil {
			return nil, pricing.OK, fmt.Errorf("product %d: %w", ii.ProductID, ErrNotFound)
		}
		line := int64(ii.Qty) * p.PriceFen
		items = append(items, models.Item{
			ProductID: p.ID, Name: p.Name, PriceFen: p.PriceFen, Qty: ii.Qty,
			LineTotalFen: line,
		})
		goodsTotal += line
	}
	if len(items) == 0 {
		return nil, pricing.OK, ErrEmptyBasket
	}
	o := &models.Order{
		OrderNo:       "OD" + randHex(6),
		MemberID:      member.ID,
		MemberName:    member.Name,
		Items:         items,
		GoodsTotalFen: goodsTotal,
		DiscountFen:   0,
		PayFen:        goodsTotal,
		Status:        "pending",
	}
	if code != "" {
		var pc models.PromoCode
		err := s.DB.Where("code = ?", code).First(&pc).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return o, pricing.ErrNotFound, nil
		}
		if err != nil {
			return nil, pricing.OK, err
		}
		var batch models.PromoBatch
		if err := s.DB.First(&batch, pc.BatchID).Error; err != nil {
			return nil, pricing.OK, err
		}
		reason, disc := pricing.Evaluate(&batch, &pc, pricing.Context{
			MemberLevel: member.Level, Items: items, At: s.clock(),
		})
		o.PromoCodeID = &pc.ID // 即使后来被封/恢复，也保留引用，支付时重新核价
		if reason == pricing.OK {
			o.DiscountFen = disc
			o.PayFen = goodsTotal - disc
			o.Promo = snap(&batch, pc.Code, disc, s.clock())
		} else {
			return o, reason, nil
		}
	}
	return o, pricing.OK, nil
}

func snap(b *models.PromoBatch, code string, disc int64, at time.Time) *models.AppliedPromo {
	return &models.AppliedPromo{
		CodeTail:      pricing.MaskTail(code),
		BatchID:       b.ID,
		BatchName:     b.Name,
		DiscountType:  b.DiscountType,
		DiscountValue: b.DiscountValue,
		DiscountFen:   disc,
		AppliedAt:     at,
	}
}

// BanResult 封禁结果：被封的码、被重新核价的待付款单、不受影响的已付款单（进入影响清单）。
type BanResult struct {
	BannedCodeIDs  []uint    `json:"banned_code_ids"`
	RepricedOrders []string  `json:"repriced_orders"`
	PaidKept       []string  `json:"paid_orders_kept"`
	BannedAt       time.Time `json:"banned_at"`
}

// BanCodes 封禁一批码（按码 ID；整批泄露时由 handler 先展开批次）。
func (s *Service) BanCodes(ids []uint, reason string) (*BanResult, error) {
	if len(ids) == 0 {
		return &BanResult{BannedAt: s.clock()}, nil
	}
	now := s.clock()
	res := &BanResult{BannedAt: now}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// 单连接池下事务天然串行：封禁与核销不会交错出两个结果。
		q := tx.Model(&models.PromoCode{}).
			Where("id IN ? AND banned_at IS NULL", ids).
			Updates(map[string]any{"banned_at": now, "ban_reason": reason})
		if q.Error != nil {
			return q.Error
		}
		if err := tx.Model(&models.PromoCode{}).Where("id IN ? AND banned_at = ?", ids, now).Pluck("id", &res.BannedCodeIDs).Error; err != nil {
			return err
		}

		// 待付款单：重新核价，优惠清零。
		var pending []models.Order
		if err := tx.Where("status = ? AND promo_code_id IN ?", "pending", ids).
			Find(&pending).Error; err != nil {
			return err
		}
		for i := range pending {
			o := &pending[i]
			pricing.RecomputeAfterBan(o)
			// 单条 UPDATE 改写同一列：一笔订单永远只有一个折扣金额，不会出现两份。
			if err := tx.Model(&models.Order{}).Where("id = ?", o.ID).
				Updates(map[string]any{
					"promo":          nil,
					"promo_code_id":  nil,
					"discount_fen":   int64(0),
					"pay_fen":        o.GoodsTotalFen,
					"repriced_count": o.RepricedCount,
					"redeemed_at":    nil,
					"updated_at":     now,
				}).Error; err != nil {
				return err
			}
			res.RepricedOrders = append(res.RepricedOrders, o.OrderNo)
		}

		// 已付款单：原优惠原样保留，仅登记给影响清单（join banned_at 即可查到）。
		if err := tx.Model(&models.Order{}).
			Where("status = ? AND promo_code_id IN ?", "paid", ids).
			Pluck("order_no", &res.PaidKept).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// UnbanCode 误封恢复：仅限仍在有效期、且从未核销的码。
func (s *Service) UnbanCode(id uint) error {
	now := s.clock()
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var pc models.PromoCode
		if err := tx.First(&pc, id).Error; err != nil {
			return ErrNotFound
		}
		if pc.RedeemedAt != nil {
			return ErrUnbanRedeemed // 已核销的码不能重新开放
		}
		var batch models.PromoBatch
		if err := tx.First(&batch, pc.BatchID).Error; err != nil {
			return err
		}
		if now.Before(batch.ValidFrom) || now.After(batch.ValidUntil) {
			return ErrUnbanExpired
		}
		return tx.Model(&pc).Updates(map[string]any{
			"banned_at": nil, "ban_reason": "",
		}).Error
	})
}

// Pay 支付核销。receiptNo 为空则本地模拟生成；同一回执重复投递返回同一结果（幂等）。
func (s *Service) Pay(orderNo, receiptNo string) (*models.Order, bool, error) {
	if receiptNo == "" {
		receiptNo = "RC" + randHex(8)
	}
	idempotent := false
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		// 1) 回执幂等：先查回执是否已处理过。
		var existing models.PaymentReceipt
		err := tx.Where("receipt_no = ?", receiptNo).First(&existing).Error
		if err == nil {
			if existing.OrderNo != orderNo {
				return ErrBadReceipt // 回执已属于另一笔订单
			}
			idempotent = true
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var o models.Order
		if err := tx.Where("order_no = ?", orderNo).First(&o).Error; err != nil {
			return ErrNotFound
		}
		if o.Status == "paid" {
			return ErrOrderPaid // 换了一张回执来支付同一单
		}

		now := s.clock() // 服务端核销时间：支付/封禁竞态唯一裁决依据

		// 2) 待付款单按服务端此刻状态重新核价（覆盖下单时的报价快照）。
		if o.PromoCodeID != nil {
			var pc models.PromoCode
			if err := tx.First(&pc, *o.PromoCodeID).Error; err != nil {
				return err
			}
			var member models.Member
			if err := tx.First(&member, o.MemberID).Error; err != nil {
				return err
			}
			var batch models.PromoBatch
			if err := tx.First(&batch, pc.BatchID).Error; err != nil {
				return err
			}
			reason, disc := pricing.Evaluate(&batch, &pc, pricing.Context{
				MemberLevel: member.Level, Items: o.Items, At: now,
			})
			if reason == pricing.OK {
				// 3) 同码并发核销：条件 UPDATE 抢占，rows affected 必须为 1。
				q := tx.Model(&models.PromoCode{}).
					Where("id = ? AND redeemed_at IS NULL AND banned_at IS NULL", pc.ID).
					Updates(map[string]any{
						"redeemed_at": now, "redeemed_by": o.MemberID, "order_no": o.OrderNo,
					})
				if q.Error != nil {
					return q.Error
				}
				if q.RowsAffected == 1 {
					o.Promo = snap(&batch, pc.Code, disc, now)
					o.DiscountFen = disc
					o.PayFen = o.GoodsTotalFen - disc
					o.RedeemedAt = &now
				} else {
					// 已被并发的另一单核销或已封禁：本单按原价支付。
					clearPromo(&o)
				}
			} else {
				clearPromo(&o)
			}
		}

		o.Status = "paid"
		o.PaidAt = &now
		if err := tx.Save(&o).Error; err != nil {
			return err
		}
		rc := models.PaymentReceipt{
			ReceiptNo: receiptNo, OrderNo: o.OrderNo,
			AmountFen: o.PayFen, ProcessedAt: now,
		}
		if err := tx.Create(&rc).Error; err != nil {
			return err // 回执号冲突时整单回滚，不留半截状态
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	o, err := s.Order(orderNo)
	if err != nil {
		return nil, false, err
	}
	return o, idempotent, nil
}

func clearPromo(o *models.Order) {
	o.Promo = nil
	o.DiscountFen = 0
	o.PayFen = o.GoodsTotalFen
	o.RedeemedAt = nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
