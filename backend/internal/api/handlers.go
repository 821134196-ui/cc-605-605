package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"promoleak/internal/pricing"
	"promoleak/internal/service"
)

type Handler struct{ Svc *service.Service }

func New(svc *service.Service) *Handler { return &Handler{Svc: svc} }

func (h *Handler) Register(r *gin.Engine) {
	api := r.Group("/api")
	{
		api.GET("/products", h.products)
		api.GET("/members", h.members)
		api.GET("/batches", h.batches)
		api.GET("/codes", h.codes)
		api.POST("/codes/:id/ban", h.ban)
		api.POST("/codes/:id/unban", h.unban)
		api.POST("/codes/ban-batch/:batchID", h.banBatch)
		api.GET("/impact", h.impact)
		api.POST("/orders/quote", h.quote)
		api.POST("/orders", h.createOrder)
		api.GET("/orders", h.listOrders)
		api.GET("/orders/:no", h.getOrder)
		api.POST("/orders/:no/pay", h.pay)
	}
}

func (h *Handler) products(c *gin.Context) {
	ps, err := h.Svc.Products()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, ps)
}

func (h *Handler) members(c *gin.Context) {
	ms, err := h.Svc.Members()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, ms)
}

func (h *Handler) batches(c *gin.Context) {
	bs, err := h.Svc.ListBatches()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, bs)
}

func (h *Handler) codes(c *gin.Context) {
	var batchID uint
	if v := c.Query("batch_id"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			c.JSON(400, gin.H{"error": "bad batch_id"})
			return
		}
		batchID = uint(id)
	}
	cs, err := h.Svc.ListMaskedCodes(batchID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, cs)
}

type banReq struct {
	Reason string `json:"reason"`
}

func (h *Handler) ban(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var req banReq
	_ = c.ShouldBindJSON(&req)
	res, err := h.Svc.BanCodes([]uint{id}, req.Reason)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, res)
}

func (h *Handler) banBatch(c *gin.Context) {
	batchID, ok := parseIDParam(c, "batchID")
	if !ok {
		return
	}
	var req banReq
	_ = c.ShouldBindJSON(&req)
	codes, err := h.Svc.ListMaskedCodes(batchID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	ids := make([]uint, 0, len(codes))
	for _, cd := range codes {
		if cd.Status != "banned" {
			ids = append(ids, cd.ID)
		}
	}
	res, err := h.Svc.BanCodes(ids, req.Reason)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, res)
}

func (h *Handler) unban(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := h.Svc.UnbanCode(id); err != nil {
		switch {
		case errors.Is(err, service.ErrNotFound):
			c.JSON(404, gin.H{"error": "code not found"})
		case errors.Is(err, service.ErrUnbanRedeemed):
			c.JSON(409, gin.H{"error": err.Error(), "code": "already_redeemed"})
		case errors.Is(err, service.ErrUnbanExpired):
			c.JSON(409, gin.H{"error": err.Error(), "code": "out_of_validity"})
		default:
			c.JSON(500, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func (h *Handler) impact(c *gin.Context) {
	rows, err := h.Svc.ImpactList()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, rows)
}

func (h *Handler) quote(c *gin.Context) {
	var in service.CreateOrderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	o, q, err := h.Svc.PreviewQuote(in.MemberID, in.Items, in.Code)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"order_preview": o, "quote": q})
}

func (h *Handler) createOrder(c *gin.Context) {
	var in service.CreateOrderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	o, reason, err := h.Svc.CreateOrder(in)
	if err != nil {
		status := 400
		if errors.Is(err, service.ErrNotFound) {
			status = 404
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if reason != pricing.OK {
		c.JSON(409, gin.H{"error": "promo rejected", "reason": string(reason)})
		return
	}
	c.JSON(201, o)
}

func (h *Handler) listOrders(c *gin.Context) {
	os2, err := h.Svc.ListOrders()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, os2)
}

func (h *Handler) getOrder(c *gin.Context) {
	o, err := h.Svc.Order(c.Param("no"))
	if err != nil {
		c.JSON(404, gin.H{"error": "order not found"})
		return
	}
	c.JSON(200, o)
}

type payReq struct {
	ReceiptNo string `json:"receipt_no"` // 可选：本地模拟回执号；用于演示重复回执
}

func (h *Handler) pay(c *gin.Context) {
	var req payReq
	_ = c.ShouldBindJSON(&req)
	o, idem, err := h.Svc.Pay(c.Param("no"), req.ReceiptNo)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotFound):
			c.JSON(404, gin.H{"error": "order not found"})
		case errors.Is(err, service.ErrOrderPaid):
			c.JSON(409, gin.H{"error": err.Error(), "code": "already_paid"})
		case errors.Is(err, service.ErrBadReceipt):
			c.JSON(409, gin.H{"error": err.Error(), "code": "receipt_conflict"})
		default:
			c.JSON(500, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(200, gin.H{"order": o, "idempotent": idem})
}

func parseID(c *gin.Context) (uint, bool) {
	return parseIDParam(c, "id")
}

func parseIDParam(c *gin.Context, key string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param(key), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad id"})
		return 0, false
	}
	return uint(id), true
}
