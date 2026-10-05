package models

import "time"

// Member 会员。
type Member struct {
	ID        uint   `gorm:"primarykey" json:"id"`
	Name      string `json:"name"`
	Level     string `gorm:"index" json:"level"` // gold / silver / normal
	CreatedAt time.Time
}

// Product 商品。
type Product struct {
	ID        uint   `gorm:"primarykey" json:"id"`
	Name      string `json:"name"`
	PriceFen  int64  `json:"price_fen"` // 单价（分）
	CreatedAt time.Time
}

// PromoBatch 折扣码批次：同一批次共享适用会员、商品、有效期与优惠规则。
type PromoBatch struct {
	ID            uint   `gorm:"primarykey" json:"id"`
	Name          string `json:"name"`
	ApplicableTo  string // 适用会员级别，逗号分隔，"*" 表示全部
	ProductIDs    string // 适用商品，逗号分隔，"*" 表示全部
	ValidFrom     time.Time
	ValidUntil    time.Time
	DiscountType  string // percent（按比例减）/ fixed（立减，单位：分）
	DiscountValue int64  // percent: 百分比 0-100；fixed: 立减金额（分）
	CreatedAt     time.Time
}

// PromoCode 单个折扣码。一个码只能被成功核销一次。
type PromoCode struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	BatchID    uint       `gorm:"index" json:"batch_id"`
	Code       string     `gorm:"uniqueIndex;size:64" json:"-"` // 明文不出现在普通列表
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`
	RedeemedBy *uint      `json:"-"`
	OrderNo    *string    `gorm:"size:32;index" json:"order_no,omitempty"`
	BannedAt   *time.Time `json:"banned_at,omitempty"`
	BanReason  string     `json:"ban_reason,omitempty"`
	CreatedAt  time.Time
}

// MaskedCode 普通列表对外形态：只暴露码尾号。
type MaskedCode struct {
	ID            uint       `json:"id"`
	BatchID       uint       `json:"batch_id"`
	BatchName     string     `json:"batch_name"`
	CodeTail      string     `json:"code_tail"` // 末 4 位
	Status        string     `json:"status"`    // active / banned / redeemed / expired
	RedeemedAt    *time.Time `json:"redeemed_at,omitempty"`
	BannedAt      *time.Time `json:"banned_at,omitempty"`
	BanReason     string     `json:"ban_reason,omitempty"`
	OrderNo       *string    `json:"order_no,omitempty"`
	ValidFrom     time.Time  `json:"valid_from"`
	ValidUntil    time.Time  `json:"valid_until"`
	DiscountType  string     `json:"discount_type"`
	DiscountValue int64      `json:"discount_value"`
}

// AppliedPromo 订单采用的优惠规则快照（订单详情中保留，不受后续封禁影响）。
type AppliedPromo struct {
	CodeTail      string `json:"code_tail"`
	BatchID       uint   `json:"batch_id"`
	BatchName     string `json:"batch_name"`
	DiscountType  string `json:"discount_type"`
	DiscountValue int64  `json:"discount_value"`
	// 当时实际生效的优惠金额（分）——一笔订单有且仅有一个优惠金额。
	DiscountFen int64     `json:"discount_fen"`
	AppliedAt   time.Time `json:"applied_at"`
}

// Item 订单行快照。
type Item struct {
	ProductID    uint   `json:"product_id"`
	Name         string `json:"name"`
	PriceFen     int64  `json:"price_fen"`
	Qty          int    `json:"qty"`
	LineTotalFen int64  `json:"line_total_fen"`
}

type Order struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	OrderNo       string    `gorm:"uniqueIndex;size:32" json:"order_no"`
	MemberID      uint      `gorm:"index" json:"member_id"`
	MemberName    string    `json:"member_name"`
	Items         JSONItems `json:"items"`
	GoodsTotalFen int64     `json:"goods_total_fen"`
	// DiscountFen 是订单唯一的优惠金额：未优惠=0；重新核价时在单条 UPDATE 中改写，不产生第二份。
	DiscountFen int64         `gorm:"default:0" json:"discount_fen"`
	PayFen      int64         `json:"pay_fen"`
	Status      string        `gorm:"index" json:"status"` // pending / paid / cancelled
	Promo       *AppliedPromo `gorm:"serializer:json" json:"promo,omitempty"`
	PromoCodeID *uint         `gorm:"index" json:"promo_code_id,omitempty"`
	// RedeemedAt：服务端核销时间，支付与封禁同时发生时按它定边界。
	RedeemedAt    *time.Time `gorm:"index" json:"redeemed_at,omitempty"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	RepricedCount int        `gorm:"default:0" json:"repriced_count"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PaymentReceipt 支付回执（本地模拟）。按回执号幂等：同一回执重复投递不产生第二笔扣款/第二份优惠。
type PaymentReceipt struct {
	ID          uint   `gorm:"primarykey"`
	ReceiptNo   string `gorm:"uniqueIndex;size:64"`
	OrderNo     string `gorm:"size:32;index"`
	AmountFen   int64
	ProcessedAt time.Time
}
