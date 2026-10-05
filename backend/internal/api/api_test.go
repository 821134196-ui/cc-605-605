package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"promoleak/internal/service"
	"promoleak/internal/store"
)

func setupRouter(t *testing.T) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := store.Open(fmt.Sprintf("file:api_%s?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_")))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Seed(db); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	New(service.New(db)).Register(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path string, body any, cookie string) (int, map[string]any) {
	st, raw, _ := doRaw(t, h, method, path, body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return st, out
}

func doRaw(t *testing.T, h http.Handler, method, path string, body any) (int, []byte, http.Header) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes(), w.Header()
}

func mustStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("key %q missing in %v", key, m)
	}
	return v
}

// HTTP 端到端：下单 → 支付 → 封禁 → 影响清单包含原优惠；列表接口不泄漏完整码。
func TestHTTP_FullLifecycle(t *testing.T) {
	h := setupRouter(t)

	// 码列表只有尾号
	st, codesRaw, _ := doRaw(t, h, "GET", "/api/codes", nil)
	if st != 200 {
		t.Fatalf("codes status %d", st)
	}
	var codesArr []map[string]any
	if err := json.Unmarshal(codesRaw, &codesArr); err != nil {
		t.Fatal(err)
	}
	for _, c := range codesArr {
		tail := c["code_tail"].(string)
		if tail == "ANNIV-8X4K-29FD-LQ17" {
			t.Fatal("list API leaked full code")
		}
		if len(tail) < 4 {
			t.Fatalf("tail too short: %s", tail)
		}
		if _, leaked := c["code"]; leaked {
			t.Fatal("raw code field present in list API")
		}
	}

	// 下单（金卡 + 商品1、2 + 8折码）
	st, body := do(t, h, "POST", "/api/orders", map[string]any{
		"member_id": 1,
		"items":     []map[string]any{{"product_id": 1, "qty": 1}, {"product_id": 2, "qty": 1}},
		"code":      "ANNIV-8X4K-29FD-LQ17",
	}, "")
	if st != http.StatusCreated {
		t.Fatalf("create status %d body=%v", st, body)
	}
	orderNo := mustStr(t, body, "order_no")
	if disc := body["discount_fen"].(float64); disc != 1000 {
		t.Fatalf("discount = %v", disc)
	}

	// 支付
	st, body = do(t, h, "POST", "/api/orders/"+orderNo+"/pay", map[string]any{"receipt_no": "RC-E2E-1"}, "")
	if st != 200 || body["idempotent"].(bool) {
		t.Fatalf("pay status=%d body=%v", st, body)
	}

	// 重复回执：200 + idempotent=true，金额不变
	st, body2 := do(t, h, "POST", "/api/orders/"+orderNo+"/pay", map[string]any{"receipt_no": "RC-E2E-1"}, "")
	if st != 200 || !body2["idempotent"].(bool) {
		t.Fatalf("duplicate receipt status=%d body=%v", st, body2)
	}
	if body2["order"].(map[string]any)["pay_fen"].(float64) != body["order"].(map[string]any)["pay_fen"].(float64) {
		t.Fatal("amount changed on duplicate receipt")
	}

	// 封禁已核销的码
	st, _ = do(t, h, "POST", "/api/codes/1/ban", map[string]any{"reason": "e2e泄露"}, "")
	if st != 200 {
		t.Fatalf("ban status %d", st)
	}

	// 误封恢复已核销码 → 409
	st, body = do(t, h, "POST", "/api/codes/1/unban", map[string]any{}, "")
	if st != 409 || body["code"] != "already_redeemed" {
		t.Fatalf("unban redeemed want 409 already_redeemed, got %d %v", st, body)
	}

	// 影响清单（顶层数组）
	st, raw, _ := doRaw(t, h, "GET", "/api/impact", nil)
	if st != 200 {
		t.Fatalf("impact status %d", st)
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("impact not array: %v", err)
	}
	if len(arr) != 1 {
		t.Fatalf("impact rows = %d", len(arr))
	}
	row := arr[0]
	if row["order_no"] != orderNo || row["code_tail"] != "****************LQ17" {
		t.Fatalf("impact row %v", row)
	}
	promo := row["promo"].(map[string]any)
	if promo["discount_fen"].(float64) != 1000 || promo["batch_name"] == "" {
		t.Fatalf("impact promo snapshot wrong: %v", promo)
	}

	// 封禁后新建的待付款单不能用该码
	st, body = do(t, h, "POST", "/api/orders", map[string]any{
		"member_id": 1,
		"items":     []map[string]any{{"product_id": 1, "qty": 1}},
		"code":      "ANNIV-8X4K-29FD-LQ17",
	}, "")
	if st != 409 || body["reason"] != "code_banned" {
		t.Fatalf("banned code create want 409 code_banned, got %d %v", st, body)
	}
}

// HTTP 层并发：同一码两单同时支付，恰好一单拿到折扣。
func TestHTTP_ConcurrentRedeem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := store.Open("file:api_concurrent?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Seed(db)
	r := gin.New()
	svc := service.New(db)
	New(svc).Register(r)

	mkOrder := func(memberID uint) string {
		st, body := do(t, r, "POST", "/api/orders", map[string]any{
			"member_id": memberID,
			"items":     []map[string]any{{"product_id": 1, "qty": 1}, {"product_id": 2, "qty": 1}},
			"code":      "NEWBIE-5K2L-0002",
		}, "")
		if st != http.StatusCreated {
			t.Fatalf("create %d: %d %v", memberID, st, body)
		}
		return mustStr(t, body, "order_no")
	}
	n1, n2 := mkOrder(1), mkOrder(2)

	var wg sync.WaitGroup
	wg.Add(2)
	var d1, d2 float64
	go func() {
		defer wg.Done()
		_, b := do(t, r, "POST", "/api/orders/"+n1+"/pay", map[string]any{"receipt_no": "RC-C1"}, "")
		d1 = b["order"].(map[string]any)["discount_fen"].(float64)
	}()
	go func() {
		defer wg.Done()
		_, b := do(t, r, "POST", "/api/orders/"+n2+"/pay", map[string]any{"receipt_no": "RC-C2"}, "")
		d2 = b["order"].(map[string]any)["discount_fen"].(float64)
	}()
	wg.Wait()
	if d1+d2 != 1000 {
		t.Fatalf("exactly one 1000-fen discount expected, got %f + %f", d1, d2)
	}
}
