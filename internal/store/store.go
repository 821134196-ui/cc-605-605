package store

import (
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"discountguard/internal/models"
)

// Open 打开/创建 SQLite 数据库。
// WAL + 串行访问已经是 SQLite 的常态；服务层另用写互斥保护“读-核价-写”序列。
func Open(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.AutoMigrate(
		&models.Product{}, &models.Batch{}, &models.Code{},
		&models.Order{}, &models.PriceHistory{}, &models.Payment{},
	); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// Seed 写入演示用商品、批次与码（仅在库为空时）。
func Seed(db *gorm.DB) error {
	var n int64
	db.Model(&models.Product{}).Count(&n)
	if n > 0 {
		return nil
	}
	now := time.Now()
	products := []models.Product{
		{Name: "拿铁咖啡", Price: 3000},
		{Name: "可颂面包", Price: 1800},
		{Name: "挂耳咖啡礼盒", Price: 9800},
	}
	if err := db.Create(&products).Error; err != nil {
		return err
	}

	batches := []models.Batch{
		{
			Name:          "新客8折券",
			DiscountType:  "percent",
			DiscountValue: 20,
			MinAmount:     2000,
			MemberLevels:  []string{"normal", "silver", "gold"},
			ProductIDs:    nil, // 全场商品
			ValidFrom:     now.Add(-24 * time.Hour),
			ValidUntil:    now.Add(7 * 24 * time.Hour),
		},
		{
			Name:          "会员礼盒立减15",
			DiscountType:  "fixed",
			DiscountValue: 1500,
			MinAmount:     5000,
			MemberLevels:  []string{"gold"},
			ProductIDs:    []uint{products[2].ID},
			ValidFrom:     now.Add(-24 * time.Hour),
			ValidUntil:    now.Add(7 * 24 * time.Hour),
		},
		{
			Name:          "国庆限时85折",
			DiscountType:  "percent",
			DiscountValue: 15,
			MinAmount:     0,
			MemberLevels:  nil,
			ProductIDs:    nil,
			ValidFrom:     now.Add(-30 * 24 * time.Hour),
			ValidUntil:    now.Add(-1 * 24 * time.Hour), // 已过期
		},
	}
	for i := range batches {
		if err := db.Create(&batches[i]).Error; err != nil {
			return err
		}
	}
	codes := []models.Code{}
	mk := func(batchID uint, full string) models.Code {
		return models.Code{BatchID: batchID, FullCode: full, Status: models.CodeStatusActive}
	}
	codes = append(codes,
		mk(batches[0].ID, "NC-2026-8K2Q-47FD"),
		mk(batches[0].ID, "NC-2026-9W1T-82MA"),
		mk(batches[0].ID, "NC-2026-LP4R-06XZ"),
		mk(batches[1].ID, "GB-2026-GLD7-15OFF"),
		mk(batches[2].ID, "ND-2026-OLD1-EXPIRED"),
	)
	if err := db.Create(&codes).Error; err != nil {
		return err
	}
	return nil
}
