package models

import (
	"time"

	"gorm.io/gorm"
)

// 金额一律使用「分」存储，避免浮点误差。

// Product 商品目录（核价时以服务端商品价格为准）
type Product struct {
	ID    uint   `gorm:"primaryKey" json:"id"`
	Name  string `gorm:"not null" json:"name"`
	Price int64  `gorm:"not null" json:"price"` // 分
}

// Batch 折扣码批次：按批次配置适用会员、商品与有效期
type Batch struct {
	ID            uint           `gorm:"primaryKey" json:"id"`
	Name          string         `gorm:"not null" json:"name"`
	DiscountType  string         `gorm:"not null" json:"discount_type"`        // percent | fixed
	DiscountValue int64          `gorm:"not null" json:"discount_value"`       // 百分比 15 表示 15%；fixed 时为减免分数
	MinAmount     int64          `gorm:"not null" json:"min_amount"`           // 适用商品小计门槛（分），0 表示无门槛
	MemberLevels  []string       `gorm:"serializer:json" json:"member_levels"` // 空表示不限会员等级
	ProductIDs    []uint         `gorm:"serializer:json" json:"product_ids"`   // 空表示全部商品
	ValidFrom     time.Time      `gorm:"not null" json:"valid_from"`
	ValidUntil    time.Time      `gorm:"not null" json:"valid_until"`
	BannedAt      *time.Time     `json:"banned_at,omitempty"`
	BanReason     string         `json:"ban_reason,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

const (
	CodeStatusActive   = "active"
	CodeStatusRedeemed = "redeemed" // 核销成功后终态，不可重新开放
)

// Code 单个折扣码。封禁用独立的 BannedAt 标记表示：
// 已核销的码后来被封禁时，需要同时保留“已核销”和“已封禁”两个事实，
// 已付款订单才能进入影响清单追溯。
type Code struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	BatchID    uint       `gorm:"not null;index" json:"batch_id"`
	FullCode   string     `gorm:"not null;uniqueIndex" json:"-"` // 完整码绝不通过列表接口返回
	Status     string     `gorm:"not null;default:active;index" json:"status"`
	BannedAt   *time.Time `gorm:"index" json:"banned_at,omitempty"`
	BanReason  string     `json:"ban_reason,omitempty"`
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`
	OrderID    *uint      `gorm:"-" json:"order_id,omitempty"` // 核销关联订单（通过支付记录关联，这里仅展示用）
	CreatedAt  time.Time  `json:"created_at"`
}

// State 返回用于展示/判断的派生状态
func (c Code) State(batchBanned bool) string {
	banned := c.BannedAt != nil || batchBanned
	switch {
	case c.Status == CodeStatusRedeemed && banned:
		return "banned_redeemed"
	case c.Status == CodeStatusRedeemed:
		return "redeemed"
	case banned:
		return "banned"
	default:
		return "active"
	}
}

// Masked 只暴露尾号
func (c Code) Masked() string {
	tail := Tail(c.FullCode)
	return "****" + tail
}

// Tail 取码尾号（最后 4 位）
func Tail(full string) string {
	r := []rune(full)
	if len(r) <= 4 {
		return string(r)
	}
	return string(r[len(r)-4:])
}

// OrderItem 订单行快照
type OrderItem struct {
	ProductID uint   `json:"product_id"`
	Name      string `json:"name"`
	UnitPrice int64  `json:"unit_price"`
	Qty       int    `json:"qty"`
	LineTotal int64  `json:"line_total"`
}

// RuleSnapshot 下单/核价时刻采用的优惠规则与金额快照
type RuleSnapshot struct {
	BatchID        uint      `json:"batch_id"`
	BatchName      string    `json:"batch_name"`
	CodeID         uint      `json:"code_id"`
	CodeTail       string    `json:"code_tail"`
	DiscountType   string    `json:"discount_type"`
	DiscountValue  int64     `json:"discount_value"`
	MinAmount      int64     `json:"min_amount"`
	MemberLevels   []string  `json:"member_levels"`
	ProductIDs     []uint    `json:"product_ids"`
	EligibleAmount int64     `json:"eligible_amount"`
	OriginalAmount int64     `json:"original_amount"`
	DiscountAmount int64     `json:"discount_amount"`
	FinalAmount    int64     `json:"final_amount"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
}

const (
	OrderStatusPending = "pending"
	OrderStatusPaid    = "paid"
)

// Order 订单。paid 之后金额与规则快照不可变。
type Order struct {
	ID             uint           `gorm:"primaryKey" json:"id"`
	OrderNo        string         `gorm:"not null;uniqueIndex" json:"order_no"`
	MemberLevel    string         `gorm:"not null" json:"member_level"`
	Status         string         `gorm:"not null;default:pending;index" json:"status"`
	Items          []OrderItem    `gorm:"serializer:json;not null" json:"items"`
	OriginalAmount int64          `gorm:"not null" json:"original_amount"`
	DiscountAmount int64          `gorm:"not null" json:"discount_amount"`
	FinalAmount    int64          `gorm:"not null" json:"final_amount"`
	CodeID         *uint          `gorm:"index" json:"code_id,omitempty"`
	CodeTail       string         `json:"code_tail,omitempty"` // 列表/详情只留尾号
	BatchID        *uint          `gorm:"index" json:"batch_id,omitempty"`
	AppliedRule    *RuleSnapshot  `gorm:"serializer:json" json:"applied_rule,omitempty"`
	RejectReason   string         `json:"reject_reason,omitempty"` // 当前核价未生效的原因码
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	PaidAt         *time.Time     `gorm:"index" json:"paid_at,omitempty"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
}

// PriceHistory 每次核价的只追加历史；一笔订单任一时刻只有一个生效折扣金额，
// 历史记录保留变化轨迹，支付后不再追加。
type PriceHistory struct {
	ID             uint          `gorm:"primaryKey" json:"id"`
	OrderID        uint          `gorm:"not null;index" json:"order_id"`
	OriginalAmount int64         `gorm:"not null" json:"original_amount"`
	DiscountAmount int64         `gorm:"not null" json:"discount_amount"`
	FinalAmount    int64         `gorm:"not null" json:"final_amount"`
	AppliedRule    *RuleSnapshot `gorm:"serializer:json" json:"applied_rule,omitempty"`
	Reason         string        `gorm:"not null" json:"reason"`
	EvaluatedAt    time.Time     `gorm:"not null" json:"evaluated_at"`
}

const (
	ReasonInitialQuote = "INITIAL_QUOTE"
	ReasonRequote      = "REQUOTE"
	ReasonBanStripped  = "BAN_STRIPPED" // 封禁后待付款订单被重新核价
)

// Payment 本地模拟的支付回执。ReceiptNo 唯一索引保证重复回执幂等。
type Payment struct {
	ID             uint          `gorm:"primaryKey" json:"id"`
	ReceiptNo      string        `gorm:"not null;uniqueIndex" json:"receipt_no"`
	OrderID        uint          `gorm:"not null;index" json:"order_id"`
	OriginalAmount int64         `gorm:"not null" json:"original_amount"`
	DiscountAmount int64         `gorm:"not null" json:"discount_amount"`
	PaidAmount     int64         `gorm:"not null" json:"paid_amount"`
	CodeID         *uint         `json:"code_id"`
	AppliedRule    *RuleSnapshot `gorm:"serializer:json" json:"applied_rule,omitempty"`
	Result         string        `gorm:"not null;default:SUCCESS" json:"result"`
	CreatedAt      time.Time     `gorm:"not null" json:"created_at"` // 即服务端核销时间
}
