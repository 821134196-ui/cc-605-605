package app

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

func RegisterRoutes(r *gin.Engine, svc *Service) {
	api := r.Group("/api")
	{
		api.GET("/products", wrap(svc.handleListProducts))
		api.GET("/batches", wrap(svc.handleListBatches))
		api.POST("/batches/:id/ban", wrap(svc.handleBanBatch))
		api.POST("/batches/:id/restore", wrap(svc.handleRestoreBatch))
		api.POST("/batches/:id/codes", wrap(svc.handleIssueCode))
		api.GET("/codes", wrap(svc.handleListCodes))
		api.POST("/codes/:id/ban", wrap(svc.handleBanCode))
		api.POST("/codes/:id/restore", wrap(svc.handleRestoreCode))
		api.POST("/quote", wrap(svc.handleQuote))
		api.POST("/orders", wrap(svc.handleCreateOrder))
		api.GET("/orders", wrap(svc.handleListOrders))
		api.GET("/orders/:id", wrap(svc.handleGetOrder))
		api.POST("/orders/:id/requote", wrap(svc.handleRequote))
		api.POST("/orders/:id/pay", wrap(svc.handlePay))
		api.GET("/payments", wrap(svc.handleListPayments))
		api.GET("/impact", wrap(svc.handleImpact))
	}
}

type hFunc func(*gin.Context) (any, error)

func wrap(h hFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := h(c)
		if err != nil {
			status := http.StatusBadRequest
			switch {
			case errors.Is(err, ErrOrderNotFound), errors.Is(err, ErrCodeNotFound), errors.Is(err, ErrBatchNotFound):
				status = http.StatusNotFound
			case errors.Is(err, ErrReceiptConflict), errors.Is(err, ErrAmountMismatch), errors.Is(err, ErrRestoreRule), errors.Is(err, ErrCodeAlreadyRedeemed):
				status = http.StatusConflict
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		if v == nil {
			c.Status(http.StatusNoContent)
			return
		}
		c.JSON(http.StatusOK, v)
	}
}

func idParam(c *gin.Context, name string) (uint, error) {
	v, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil {
		return 0, errors.New("invalid id")
	}
	return uint(v), nil
}

type banReq struct {
	Reason string `json:"reason"`
}

func (s *Service) handleListProducts(c *gin.Context) (any, error) {
	return s.ListProducts()
}
func (s *Service) handleListBatches(c *gin.Context) (any, error) {
	return s.ListBatches()
}
func (s *Service) handleListCodes(c *gin.Context) (any, error) {
	var bid uint
	if v := c.Query("batch_id"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return nil, errors.New("invalid batch_id")
		}
		bid = uint(n)
	}
	return s.ListCodes(bid)
}
func (s *Service) handleBanBatch(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	var req banReq
	_ = c.ShouldBindJSON(&req)
	if req.Reason == "" {
		req.Reason = "运营批量封禁"
	}
	return s.BanBatch(id, req.Reason, time.Now())
}
func (s *Service) handleIssueCode(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	full, err := s.IssueCode(id, time.Now())
	if err != nil {
		return nil, err
	}
	return full, nil
}
func (s *Service) handleRestoreBatch(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	n, err := s.RestoreBatch(id, time.Now())
	if err != nil {
		return nil, err
	}
	return gin.H{"restored": n}, nil
}
func (s *Service) handleBanCode(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	var req banReq
	_ = c.ShouldBindJSON(&req)
	if req.Reason == "" {
		req.Reason = "折扣码泄露，单码封禁"
	}
	return s.BanCode(id, req.Reason, time.Now())
}
func (s *Service) handleRestoreCode(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	if err := s.RestoreCode(id, time.Now()); err != nil {
		return nil, err
	}
	return gin.H{"restored": true}, nil
}
func (s *Service) handleQuote(c *gin.Context) (any, error) {
	var in QuoteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		return nil, err
	}
	return s.Quote(in, time.Now())
}
func (s *Service) handleCreateOrder(c *gin.Context) (any, error) {
	var in QuoteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		return nil, err
	}
	return s.CreateOrder(in, time.Now())
}
func (s *Service) handleListOrders(c *gin.Context) (any, error) { return s.ListOrders() }
func (s *Service) handleGetOrder(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	o, hist, pay, err := s.GetOrder(id)
	if err != nil {
		return nil, err
	}
	return gin.H{"order": o, "price_history": hist, "payment": pay}, nil
}
func (s *Service) handleRequote(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	o, q, changed, err := s.Requote(id, time.Now())
	if err != nil {
		return nil, err
	}
	return gin.H{"order": o, "quote": q, "changed": changed}, nil
}
func (s *Service) handlePay(c *gin.Context) (any, error) {
	id, err := idParam(c, "id")
	if err != nil {
		return nil, err
	}
	var in PayInput
	_ = c.ShouldBindJSON(&in)
	return s.Pay(id, in, time.Now())
}
func (s *Service) handleListPayments(c *gin.Context) (any, error) { return s.ListPayments() }
func (s *Service) handleImpact(c *gin.Context) (any, error) {
	var bid, cid uint
	if v := c.Query("batch_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			bid = uint(n)
		}
	}
	if v := c.Query("code_id"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			cid = uint(n)
		}
	}
	return s.ImpactList(bid, cid)
}
