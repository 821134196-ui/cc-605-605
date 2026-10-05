package app

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"discountguard/internal/models"
	"discountguard/internal/pricing"
)

var (
	ErrOrderNotFound       = errors.New("order not found")
	ErrCodeNotFound        = errors.New("code not found")
	ErrBatchNotFound       = errors.New("batch not found")
	ErrReceiptConflict     = errors.New("receipt already used by another order")
	ErrAmountMismatch      = errors.New("paid amount mismatch")
	ErrRestoreRule         = errors.New("code can only be restored when unused and still valid")
	ErrCodeAlreadyRedeemed = errors.New("code has already been redeemed")
)

// Service 是所有写操作的入口。
// 单一写互斥把“读取状态 → 核价 → 条件写库”变成服务端的串行序列，
// 支付与封禁谁先拿到锁，就按谁的核销时间点确定结果。
type Service struct {
	db *gorm.DB
	mu sync.Mutex
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

// ---------------- 查询 ----------------

func (s *Service) ListProducts() ([]models.Product, error) {
	var ps []models.Product
	err := s.db.Order("id").Find(&ps).Error
	return ps, err
}

type CodeView struct {
	models.Code
	Tail       string    `json:"tail"`
	BatchName  string    `json:"batch_name"`
	State      string    `json:"state"`
	ValidUntil time.Time `json:"valid_until"`
	OrderNo    string    `json:"order_no,omitempty"`
}

func (s *Service) ListCodes(batchID uint) ([]CodeView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var codes []models.Code
	q := s.db.Order("id")
	if batchID > 0 {
		q = q.Where("batch_id = ?", batchID)
	}
	if err := q.Find(&codes).Error; err != nil {
		return nil, err
	}
	batches := map[uint]models.Batch{}
	var bs []models.Batch
	if err := s.db.Find(&bs).Error; err != nil {
		return nil, err
	}
	for _, b := range bs {
		batches[b.ID] = b
	}
	orderNos := map[uint]string{}
	var paidOrders []models.Order
	s.db.Where("code_id IS NOT NULL").Find(&paidOrders)
	for _, o := range paidOrders {
		if o.CodeID != nil {
			orderNos[*o.CodeID] = o.OrderNo
		}
	}
	views := make([]CodeView, 0, len(codes))
	for _, c := range codes {
		b := batches[c.BatchID]
		views = append(views, CodeView{
			Code:       c,
			Tail:       c.Masked(),
			BatchName:  b.Name,
			State:      c.State(b.BannedAt != nil),
			ValidUntil: b.ValidUntil,
			OrderNo:    orderNos[c.ID],
		})
	}
	return views, nil
}

type BatchView struct {
	models.Batch
	State string `json:"state"` // active | banned
}

func (s *Service) ListBatches() ([]BatchView, error) {
	var bs []models.Batch
	if err := s.db.Order("id").Find(&bs).Error; err != nil {
		return nil, err
	}
	out := make([]BatchView, len(bs))
	for i, b := range bs {
		state := "active"
		if b.BannedAt != nil {
			state = "banned"
		}
		out[i] = BatchView{Batch: b, State: state}
	}
	return out, nil
}

func (s *Service) GetOrder(id uint) (*models.Order, []models.PriceHistory, *models.Payment, error) {
	var o models.Order
	if err := s.db.First(&o, id).Error; err != nil {
		return nil, nil, nil, ErrOrderNotFound
	}
	var hist []models.PriceHistory
	s.db.Where("order_id = ?", id).Order("id").Find(&hist)
	var pay *models.Payment
	var p models.Payment
	if err := s.db.Where("order_id = ?", id).First(&p).Error; err == nil {
		pay = &p
	}
	return &o, hist, pay, nil
}

func (s *Service) ListOrders() ([]models.Order, error) {
	var os []models.Order
	err := s.db.Order("id desc").Find(&os).Error
	return os, err
}

func (s *Service) ListPayments() ([]models.Payment, error) {
	var ps []models.Payment
	err := s.db.Order("id").Find(&ps).Error
	return ps, err
}

// ---------------- 核价 / 下单 ----------------

type QuoteInput struct {
	MemberLevel string           `json:"member_level"`
	Items       []QuoteItemInput `json:"items"`
	Code        string           `json:"code"` // 完整码，仅用于本次核价
}
type QuoteItemInput struct {
	ProductID uint `json:"product_id"`
	Qty       int  `json:"qty"`
}

// loadQuoteContext 在事务内按完整码读取码与批次（完整码不作为列表数据暴露）
func (s *Service) loadQuoteContext(tx *gorm.DB, fullCode string) (*models.Code, *models.Batch) {
	if fullCode == "" {
		return nil, nil
	}
	var c models.Code
	if err := tx.Where("full_code = ?", fullCode).First(&c).Error; err != nil {
		return nil, nil
	}
	var b models.Batch
	if err := tx.First(&b, c.BatchID).Error; err != nil {
		return &c, nil
	}
	return &c, &b
}

func (s *Service) buildItems(input []QuoteItemInput) ([]models.OrderItem, error) {
	if len(input) == 0 {
		return nil, errors.New("empty items")
	}
	items := make([]models.OrderItem, 0, len(input))
	var total int64
	for _, in := range input {
		if in.Qty <= 0 {
			return nil, errors.New("qty must be positive")
		}
		var p models.Product
		if err := s.db.First(&p, in.ProductID).Error; err != nil {
			return nil, fmt.Errorf("product %d not found", in.ProductID)
		}
		line := p.Price * int64(in.Qty)
		total += line
		items = append(items, models.OrderItem{
			ProductID: p.ID, Name: p.Name, UnitPrice: p.Price, Qty: in.Qty, LineTotal: line,
		})
	}
	if total <= 0 {
		return nil, errors.New("order total must be positive")
	}
	return items, nil
}

// Quote 只核价、不落单
func (s *Service) Quote(in QuoteInput, now time.Time) (pricing.Quote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.buildItems(in.Items)
	if err != nil {
		return pricing.Quote{}, err
	}
	c, b := s.loadQuoteContext(s.db, in.Code)
	return pricing.Evaluate(items, in.MemberLevel, c, b, now), nil
}

// CreateOrder 服务端重新核价后创建待付款订单
func (s *Service) CreateOrder(in QuoteInput, now time.Time) (*models.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.buildItems(in.Items)
	if err != nil {
		return nil, err
	}
	var c *models.Code
	var b *models.Batch
	var order *models.Order
	err = s.db.Transaction(func(tx *gorm.DB) error {
		c, b = s.loadQuoteContext(tx, in.Code)
		q := pricing.Evaluate(items, in.MemberLevel, c, b, now)
		o := models.Order{
			OrderNo:        newOrderNo(now),
			MemberLevel:    in.MemberLevel,
			Status:         models.OrderStatusPending,
			Items:          items,
			OriginalAmount: q.OriginalAmount,
			DiscountAmount: q.DiscountAmount,
			FinalAmount:    q.FinalAmount,
			RejectReason:   rejectOf(q),
		}
		if q.Rule != nil {
			o.CodeID = &q.Rule.CodeID
			o.CodeTail = q.Rule.CodeTail
			o.BatchID = &q.Rule.BatchID
			o.AppliedRule = q.Rule
		}
		if err := tx.Create(&o).Error; err != nil {
			return err
		}
		hist := models.PriceHistory{
			OrderID: o.ID, OriginalAmount: q.OriginalAmount, DiscountAmount: q.DiscountAmount,
			FinalAmount: q.FinalAmount, AppliedRule: q.Rule,
			Reason: models.ReasonInitialQuote, EvaluatedAt: now,
		}
		if err := tx.Create(&hist).Error; err != nil {
			return err
		}
		order = &o
		return nil
	})
	return order, err
}

var orderSeq uint64

// newOrderNo 纳秒时间戳 + 进程内递增序列号，保证高并发下订单号唯一
func newOrderNo(now time.Time) string {
	seq := atomic.AddUint64(&orderSeq, 1) % 100000
	return fmt.Sprintf("ORD-%s-%05d", now.Format("0102150405.000000"), seq)
}

func rejectOf(q pricing.Quote) string {
	if q.Rule != nil {
		return ""
	}
	return q.Reason
}

// requotePendingLocked 在事务内对一笔待付款订单按当前状态重新核价，
// 若折扣金额发生变化则原子更新订单并只追加一条价格历史。
// 返回 (报价, 是否发生变化)。
func requotePendingLocked(tx *gorm.DB, o *models.Order, now time.Time, reason string) (pricing.Quote, bool, error) {
	var c *models.Code
	var b *models.Batch
	if o.CodeID != nil {
		var cc models.Code
		if err := tx.First(&cc, *o.CodeID).Error; err == nil {
			c = &cc
			var bb models.Batch
			if err := tx.First(&bb, cc.BatchID).Error; err == nil {
				b = &bb
			}
		}
	}
	q := pricing.Evaluate(o.Items, o.MemberLevel, c, b, now)
	changed := q.DiscountAmount != o.DiscountAmount || q.FinalAmount != o.FinalAmount ||
		(q.Rule == nil) != (o.AppliedRule == nil)
	if changed {
		o.OriginalAmount = q.OriginalAmount
		o.DiscountAmount = q.DiscountAmount
		o.FinalAmount = q.FinalAmount
		o.RejectReason = rejectOf(q)
		if q.Rule == nil {
			// 折扣被剥离：清掉生效规则，但保留 batch/code 关联与码尾号，供追溯
			o.AppliedRule = nil
			o.CodeTail = codeTailSafe(c)
		} else {
			o.AppliedRule = q.Rule
			o.CodeTail = q.Rule.CodeTail
		}
		if err := tx.Model(&models.Order{}).Where("id = ?", o.ID).
			Updates(map[string]any{
				"original_amount": o.OriginalAmount,
				"discount_amount": o.DiscountAmount,
				"final_amount":    o.FinalAmount,
				"applied_rule":    o.AppliedRule,
				"reject_reason":   o.RejectReason,
				"code_tail":       o.CodeTail,
			}).Error; err != nil {
			return q, false, err
		}
		hist := models.PriceHistory{
			OrderID: o.ID, OriginalAmount: q.OriginalAmount, DiscountAmount: q.DiscountAmount,
			FinalAmount: q.FinalAmount, AppliedRule: q.Rule, Reason: reason, EvaluatedAt: now,
		}
		if err := tx.Create(&hist).Error; err != nil {
			return q, false, err
		}
	}
	return q, changed, nil
}

func codeTailSafe(c *models.Code) string {
	if c == nil {
		return ""
	}
	return c.Masked()
}

// Requote 手动触发待付款订单重新核价
func (s *Service) Requote(orderID uint, now time.Time) (*models.Order, pricing.Quote, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var o models.Order
	if err := s.db.First(&o, orderID).Error; err != nil {
		return nil, pricing.Quote{}, false, ErrOrderNotFound
	}
	if o.Status != models.OrderStatusPending {
		return &o, pricing.Quote{}, false, errors.New("only pending orders can be re-quoted")
	}
	var q pricing.Quote
	var changed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		q, changed, err = requotePendingLocked(tx, &o, now, models.ReasonRequote)
		return err
	})
	return &o, q, changed, err
}

// ---------------- 封禁 / 恢复 ----------------

type BanResult struct {
	BannedCodes    int `json:"banned_codes"`
	RequotedOrders int `json:"requoted_orders"` // 被重新核价的待付款订单（折扣被剥离）
	AffectedPaid   int `json:"affected_paid"`   // 已付款：保留快照，进入影响清单
}

func (s *Service) BanBatch(batchID uint, reason string, now time.Time) (*BanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &BanResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var b models.Batch
		if err := tx.First(&b, batchID).Error; err != nil {
			return ErrBatchNotFound
		}
		if err := tx.Model(&models.Batch{}).Where("id = ? AND banned_at IS NULL", batchID).
			Updates(map[string]any{"banned_at": now, "ban_reason": reason}).Error; err != nil {
			return err
		}
		// 封禁以批次为单位生效（核价/核销都会判定 batch.BannedAt），
		// 不连坐到码，避免解封时把已核销的码重新打开。
		var n int64
		tx.Model(&models.Code{}).Where("batch_id = ?", batchID).Count(&n)
		res.BannedCodes = int(n)
		return stripPending(tx, "batch_id = ?", []any{batchID}, now, res)
	})
	return res, err
}

func resInt(_ *BanResult, _ *int) *int64 { return nil }

func (s *Service) BanCode(codeID uint, reason string, now time.Time) (*BanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := &BanResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var c models.Code
		if err := tx.First(&c, codeID).Error; err != nil {
			return ErrCodeNotFound
		}
		if err := tx.Model(&models.Code{}).Where("id = ? AND banned_at IS NULL", codeID).
			Updates(map[string]any{"banned_at": now, "ban_reason": reason}).Error; err != nil {
			return err
		}
		res.BannedCodes = 1
		return stripPending(tx, "code_id = ?", []any{codeID}, now, res)
	})
	return res, err
}

// stripPending 把命中条件的待付款订单全部按“已封禁”重新核价（折扣剥离），
// 同时统计已付款受影响订单数（已付款的金额绝不修改）。
func stripPending(tx *gorm.DB, cond string, args []any, now time.Time, res *BanResult) error {
	var pendings []models.Order
	q := tx.Where("status = ?", models.OrderStatusPending)
	if err := q.Where(cond, args...).Find(&pendings).Error; err != nil {
		return err
	}
	for i := range pendings {
		if _, _, err := requotePendingLocked(tx, &pendings[i], now, models.ReasonBanStripped); err != nil {
			return err
		}
		res.RequotedOrders++
	}
	var paidN int64
	if err := tx.Model(&models.Order{}).Where("status = ?", models.OrderStatusPaid).
		Where(cond, args...).Count(&paidN).Error; err != nil {
		return err
	}
	res.AffectedPaid = int(paidN)
	return nil
}

// RestoreCode 误封恢复：仅限码未被核销（redeemed 终态不可逆）、
// 且所属批次当前未封禁且仍在有效期内。
func (s *Service) RestoreCode(codeID uint, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var c models.Code
		if err := tx.First(&c, codeID).Error; err != nil {
			return ErrCodeNotFound
		}
		var b models.Batch
		if err := tx.First(&b, c.BatchID).Error; err != nil {
			return ErrBatchNotFound
		}
		switch {
		case c.Status == models.CodeStatusRedeemed:
			return fmt.Errorf("%w: 已核销的码不可重新开放", ErrRestoreRule)
		case c.BannedAt == nil:
			return fmt.Errorf("%w: 码未处于封禁状态", ErrRestoreRule)
		case b.BannedAt != nil:
			return fmt.Errorf("%w: 所属批次仍在封禁中", ErrRestoreRule)
		case now.Before(b.ValidFrom) || now.After(b.ValidUntil):
			return fmt.Errorf("%w: 批次已不在有效期", ErrRestoreRule)
		}
		return tx.Model(&models.Code{}).Where("id = ?", codeID).
			Updates(map[string]any{"banned_at": nil, "ban_reason": ""}).Error
	})
}

// RestoreBatch 解除批次封禁。码级独立封禁与 redeemed 终态均不受影响。
func (s *Service) RestoreBatch(batchID uint, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var b models.Batch
		if err := tx.First(&b, batchID).Error; err != nil {
			return ErrBatchNotFound
		}
		if now.After(b.ValidUntil) {
			return fmt.Errorf("%w: 批次已过期，解除封禁后码也不可用", ErrRestoreRule)
		}
		r := tx.Model(&models.Batch{}).Where("id = ?", batchID).
			Updates(map[string]any{"banned_at": nil, "ban_reason": ""})
		n = r.RowsAffected
		return r.Error
	})
	return n, err
}

// ---------------- 核销支付（本地模拟回执） ----------------

type PayInput struct {
	ReceiptNo  string `json:"receipt_no"`  // 可由调用方提供，用于模拟支付渠道重复回执
	PaidAmount *int64 `json:"paid_amount"` // 为空时本地模拟渠道按服务端金额确认
}

type PayResult struct {
	Order      *models.Order   `json:"order"`
	Payment    *models.Payment `json:"payment"`
	Idempotent bool            `json:"idempotent_replay"` // true=重复回执，未发生第二次扣款
	Requoted   bool            `json:"requoted_at_pay"`   // 支付时重新核价（如封禁先生效，折扣被剥离）
}

// Pay 本地模拟支付回执：服务端在这一刻完成“最终核价 → 核销码 → 出回执”。
func (s *Service) Pay(orderID uint, in PayInput, now time.Time) (*PayResult, error) {
	// 写互斥：支付与封禁并发时，先进入者决定订单适用结果
	s.mu.Lock()
	defer s.mu.Unlock()

	result := &PayResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var o models.Order
		if err := tx.First(&o, orderID).Error; err != nil {
			return ErrOrderNotFound
		}

		// 1) 回执幂等：同一回执号已存在 → 原样返回，绝不二次扣款、不改金额
		if in.ReceiptNo != "" {
			var exist models.Payment
			err := tx.Where("receipt_no = ?", in.ReceiptNo).First(&exist).Error
			if err == nil {
				if exist.OrderID != o.ID {
					return ErrReceiptConflict
				}
				var paid models.Order
				tx.First(&paid, o.ID)
				result.Order = &paid
				result.Payment = &exist
				result.Idempotent = true
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}

		if o.Status == models.OrderStatusPaid {
			// 订单已付款（无回执号的重复通知）：幂等返回既有支付
			var exist models.Payment
			if err := tx.Where("order_id = ?", o.ID).First(&exist).Error; err != nil {
				return err
			}
			result.Order = &o
			result.Payment = &exist
			result.Idempotent = true
			return nil
		}

		// 2) 支付时刻按服务端当前状态最终核价。
		//    - 码已被他单核销 → 明确拒绝（同一码只能核销成功一次），不扣款、不改价；
		//    - 封禁/过期/资格不符 → 折扣剥离，按原价收款。
		var c0 *models.Code
		var b0 *models.Batch
		if o.CodeID != nil {
			var cc models.Code
			if err := tx.First(&cc, *o.CodeID).Error; err == nil {
				c0 = &cc
				var bb models.Batch
				if err := tx.First(&bb, cc.BatchID).Error; err == nil {
					b0 = &bb
				}
			}
		}
		pre := pricing.Evaluate(o.Items, o.MemberLevel, c0, b0, now)
		if o.AppliedRule != nil && pre.Reason == pricing.ReasonCodeRedeemed {
			return ErrCodeAlreadyRedeemed
		}

		q, changed, err := requotePendingLocked(tx, &o, now, models.ReasonRequote)
		if err != nil {
			return err
		}
		result.Requoted = changed

		// 3) 码核销：仅当报价仍判定该码可用时。条件 UPDATE 是数据库层兜底，
		//    保证同一码在任何情况下只会被核销成功一次。
		var codeID *uint
		var rule *models.RuleSnapshot
		if q.Rule != nil {
			cid := q.Rule.CodeID
			res := tx.Model(&models.Code{}).
				Where("id = ? AND status = ? AND banned_at IS NULL", cid, models.CodeStatusActive).
				Updates(map[string]any{"status": models.CodeStatusRedeemed, "redeemed_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 {
				codeID = &cid
				rule = q.Rule
			} else {
				// 深度防御（互斥锁内通常不会到达）：码刚被他单核销 → 拒绝收款
				return ErrCodeAlreadyRedeemed
			}
		}

		// 4) 金额一致性：一笔订单只能按一个最终金额收款
		wantPaid := o.FinalAmount
		if in.PaidAmount != nil && *in.PaidAmount != wantPaid {
			return ErrAmountMismatch
		}

		receiptNo := in.ReceiptNo
		if receiptNo == "" {
			receiptNo = fmt.Sprintf("RCP-%d-%06d", now.UnixNano(), o.ID)
		}
		pay := models.Payment{
			ReceiptNo:      receiptNo,
			OrderID:        o.ID,
			OriginalAmount: o.OriginalAmount,
			DiscountAmount: o.DiscountAmount,
			PaidAmount:     wantPaid,
			CodeID:         codeID,
			AppliedRule:    rule,
			Result:         "SUCCESS",
			CreatedAt:      now,
		}
		if err := tx.Create(&pay).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrReceiptConflict
			}
			return err
		}
		if err := tx.Model(&models.Order{}).Where("id = ? AND status = ?", o.ID, models.OrderStatusPending).
			Updates(map[string]any{"status": models.OrderStatusPaid, "paid_at": now}).Error; err != nil {
			return err
		}
		o.Status = models.OrderStatusPaid
		o.PaidAt = &now
		result.Order = &o
		result.Payment = &pay
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// IssueCodes 发码结果
type IssuedCode struct {
	ID       uint   `json:"id"`
	FullCode string `json:"full_code"`
	Tail     string `json:"tail"`
}

// IssueCode 在指定批次下发一张新码。完整码仅此一次返回，列表中只展示尾号。
func (s *Service) IssueCode(batchID uint, now time.Time) (*IssuedCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := fmt.Sprintf("DG-%s-%05d%03d", now.Format("0102"), atomic.AddUint64(&orderSeq, 1)%100000, now.UnixNano()%1000)
	c := models.Code{BatchID: batchID, FullCode: full, Status: models.CodeStatusActive}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var b models.Batch
		if err := tx.First(&b, batchID).Error; err != nil {
			return ErrBatchNotFound
		}
		return tx.Create(&c).Error
	})
	if err != nil {
		return nil, err
	}
	return &IssuedCode{ID: c.ID, FullCode: full, Tail: models.Tail(full)}, nil
}

// ---------------- 影响清单 ----------------

type ImpactEntry struct {
	Order       models.Order    `json:"order"`
	Payment     *models.Payment `json:"payment"`
	BatchID     uint            `json:"batch_id"`
	BatchName   string          `json:"batch_name"`
	BannedAt    *time.Time      `json:"banned_at"`
	BanReason   string          `json:"ban_reason"`
	BannedScope string          `json:"banned_scope"` // batch | code
}

// ImpactList 已付款且所用码/批次事后被封禁的订单——保留当时确认的优惠快照供追溯。
// 判定来源是 payments.code_id：只有支付时真正核销了优惠的回执才进入清单，
// 封禁后被剥离折扣、按原价支付的订单不算“已使用优惠”。
func (s *Service) ImpactList(batchID, codeID uint) ([]ImpactEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var pays []models.Payment
	q := s.db.Where("code_id IS NOT NULL AND discount_amount > 0")
	if codeID > 0 {
		q = q.Where("code_id = ?", codeID)
	}
	if err := q.Order("id desc").Find(&pays).Error; err != nil {
		return nil, err
	}
	codes := map[uint]models.Code{}
	batches := map[uint]models.Batch{}
	var cs []models.Code
	s.db.Find(&cs)
	for _, c := range cs {
		codes[c.ID] = c
	}
	var bs []models.Batch
	s.db.Find(&bs)
	for _, b := range bs {
		batches[b.ID] = b
	}
	out := []ImpactEntry{}
	for _, p := range pays {
		if p.CodeID == nil {
			continue
		}
		if batchID > 0 {
			c := codes[*p.CodeID]
			if c.BatchID != batchID {
				continue
			}
		}
		var o models.Order
		if err := s.db.First(&o, p.OrderID).Error; err != nil || o.Status != models.OrderStatusPaid {
			continue
		}
		c := codes[*p.CodeID]
		b := batches[c.BatchID]
		scope := ""
		var bannedAt *time.Time
		reason := ""
		if b.BannedAt != nil {
			scope = "batch"
			bannedAt = b.BannedAt
			reason = b.BanReason
		}
		if c.BannedAt != nil && b.BannedAt == nil {
			scope = "code"
			bannedAt = c.BannedAt
			reason = c.BanReason
		}
		if scope == "" {
			continue
		}
		pay := p
		out = append(out, ImpactEntry{
			Order: o, Payment: &pay, BatchID: b.ID, BatchName: b.Name,
			BannedAt: bannedAt, BanReason: reason, BannedScope: scope,
		})
	}
	return out, nil
}
