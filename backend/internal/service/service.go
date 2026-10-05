package service

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"promoleak/internal/models"
	"promoleak/internal/pricing"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrUnbanRedeemed = errors.New("code already redeemed and cannot be reopened")
	ErrUnbanExpired  = errors.New("batch no longer within validity window")
	ErrOrderPaid     = errors.New("order already paid under another receipt")
	ErrEmptyBasket   = errors.New("empty basket")
	ErrBadReceipt    = errors.New("receipt already used by another order")
)

type Service struct {
	DB *gorm.DB
	// Now 可被测试覆盖以固定服务端时间。
	Now func() time.Time
}

func New(db *gorm.DB) *Service { return &Service{DB: db} }

// ---------- 查询 ----------

func (s *Service) Products() ([]models.Product, error) {
	var ps []models.Product
	return ps, s.DB.Order("id").Find(&ps).Error
}

func (s *Service) Members() ([]models.Member, error) {
	var ms []models.Member
	return ms, s.DB.Order("id").Find(&ms).Error
}

type BatchView struct {
	models.PromoBatch
	CodeCount int `json:"code_count"`
}

func (s *Service) ListBatches() ([]BatchView, error) {
	var batches []models.PromoBatch
	if err := s.DB.Order("id").Find(&batches).Error; err != nil {
		return nil, err
	}
	out := make([]BatchView, 0, len(batches))
	for _, b := range batches {
		var cnt int64
		s.DB.Model(&models.PromoCode{}).Where("batch_id = ?", b.ID).Count(&cnt)
		out = append(out, BatchView{PromoBatch: b, CodeCount: int(cnt)})
	}
	return out, nil
}

// ListMaskedCodes 普通列表：只有码尾号，没有明文。
func (s *Service) ListMaskedCodes(batchID uint) ([]models.MaskedCode, error) {
	q := s.DB.Table("promo_codes").
		Select("promo_codes.*, promo_batches.name as batch_name, promo_batches.valid_from, promo_batches.valid_until, promo_batches.discount_type, promo_batches.discount_value").
		Joins("join promo_batches on promo_batches.id = promo_codes.batch_id")
	if batchID > 0 {
		q = q.Where("promo_codes.batch_id = ?", batchID)
	}
	type row struct {
		models.PromoCode
		BatchName     string
		ValidFrom     time.Time
		ValidUntil    time.Time
		DiscountType  string
		DiscountValue int64
	}
	var rows []row
	if err := q.Order("promo_codes.id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]models.MaskedCode, 0, len(rows))
	for _, r := range rows {
		st := "active"
		switch {
		case r.BannedAt != nil:
			st = "banned"
		case r.RedeemedAt != nil:
			st = "redeemed"
		case now.Before(r.ValidFrom) || now.After(r.ValidUntil):
			st = "expired"
		}
		banReason := ""
		if r.BannedAt != nil {
			banReason = r.BanReason
		}
		out = append(out, models.MaskedCode{
			ID: r.ID, BatchID: r.BatchID, BatchName: r.BatchName,
			CodeTail: pricing.MaskTail(r.Code), Status: st,
			RedeemedAt: r.RedeemedAt, BannedAt: r.BannedAt, BanReason: banReason,
			OrderNo:   r.OrderNo,
			ValidFrom: r.ValidFrom, ValidUntil: r.ValidUntil,
			DiscountType: r.DiscountType, DiscountValue: r.DiscountValue,
		})
	}
	return out, nil
}

func (s *Service) Order(orderNo string) (*models.Order, error) {
	var o models.Order
	if err := s.DB.Where("order_no = ?", orderNo).First(&o).Error; err != nil {
		return nil, ErrNotFound
	}
	return &o, nil
}

func (s *Service) ListOrders() ([]models.Order, error) {
	var os []models.Order
	return os, s.DB.Order("id desc").Limit(200).Find(&os).Error
}

// ImpactRow 影响清单行：每笔已付款订单 + 它当时确认的原优惠快照。
type ImpactRow struct {
	OrderNo       string               `json:"order_no"`
	MemberName    string               `json:"member_name"`
	Status        string               `json:"status"`
	GoodsTotalFen int64                `json:"goods_total_fen"`
	DiscountFen   int64                `json:"discount_fen"`
	PayFen        int64                `json:"pay_fen"`
	RedeemedAt    *time.Time           `json:"redeemed_at"`
	PaidAt        *time.Time           `json:"paid_at"`
	BannedAt      *time.Time           `json:"banned_at"`
	BanReason     string               `json:"ban_reason"`
	CodeTail      string               `json:"code_tail"`
	BatchName     string               `json:"batch_name"`
	Promo         *models.AppliedPromo `gorm:"-" json:"promo"`
}

// ImpactList 已付款且所用码后来被封的订单——保留原优惠快照，供逐笔追溯。
func (s *Service) ImpactList() ([]ImpactRow, error) {
	type impactScan struct {
		ImpactRow
		RawCode  string `gorm:"column:raw_code"`
		RawPromo string `gorm:"column:raw_promo"`
	}
	var scans []impactScan
	err := s.DB.Table("orders").
		Select(`orders.order_no, orders.member_name, orders.status,
			orders.goods_total_fen, orders.discount_fen, orders.pay_fen,
			orders.redeemed_at, orders.paid_at,
			promo_codes.banned_at, promo_codes.ban_reason, promo_codes.code as raw_code,
			promo_batches.name as batch_name, orders.promo as raw_promo`).
		Joins("join promo_codes on promo_codes.id = orders.promo_code_id").
		Joins("join promo_batches on promo_batches.id = promo_codes.batch_id").
		Where("orders.status = ? AND promo_codes.banned_at IS NOT NULL", "paid").
		Order("orders.redeemed_at desc, orders.id desc").
		Scan(&scans).Error
	if err != nil {
		return nil, err
	}
	out := make([]ImpactRow, 0, len(scans))
	for _, sc := range scans {
		// 明文码不进 API：即使追溯清单也只给尾号。
		row := sc.ImpactRow
		row.CodeTail = pricing.MaskTail(sc.RawCode)
		if sc.RawPromo != "" {
			var ap models.AppliedPromo
			if err := json.Unmarshal([]byte(sc.RawPromo), &ap); err == nil {
				row.Promo = &ap
			}
		}
		out = append(out, row)
	}
	return out, nil
}
