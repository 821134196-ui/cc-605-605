package store

import (
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"promoleak/internal/models"
)

func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：显式开启 WAL + busy_timeout，配合事务实现并发互斥。
	sqlDB, _ := db.DB()
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	} {
		if err := db.Exec(pragma).Error; err != nil {
			return nil, err
		}
	}
	// SQLite 单文件写者：连接池收敛为 1，所有事务在同一连接上串行，
	// 配合条件 UPDATE 实现“同码并发核销只成功一次”的严格互斥。
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.Member{}, &models.Product{}, &models.PromoBatch{},
		&models.PromoCode{}, &models.Order{}, &models.PaymentReceipt{},
	); err != nil {
		return nil, err
	}
	return db, nil
}

// Seed 灌入演示数据：3 个会员、4 个商品、2 个批次共 6 个码。
func Seed(db *gorm.DB) error {
	var n int64
	db.Model(&models.PromoBatch{}).Count(&n)
	if n > 0 {
		return nil
	}
	members := []models.Member{
		{Name: "金卡会员·王芳", Level: "gold"},
		{Name: "银卡会员·李伟", Level: "silver"},
		{Name: "普通会员·周敏", Level: "normal"},
	}
	products := []models.Product{
		{Name: "拿铁（大杯）", PriceFen: 3200},
		{Name: "可颂", PriceFen: 1800},
		{Name: "咖啡豆 250g", PriceFen: 8800},
		{Name: "挂耳礼盒", PriceFen: 6600},
	}
	if err := db.Create(&members).Error; err != nil {
		return err
	}
	if err := db.Create(&products).Error; err != nil {
		return err
	}
	now := time.Now()
	batches := []models.PromoBatch{
		{
			Name: "双十周年庆·8折批次", ApplicableTo: "gold,silver",
			ProductIDs:   "*",
			ValidFrom:    now.Add(-24 * time.Hour),
			ValidUntil:   now.Add(7 * 24 * time.Hour),
			DiscountType: "percent", DiscountValue: 20, // 减 20%
		},
		{
			Name: "新客满减批次", ApplicableTo: "*",
			ProductIDs:   "1,2,4",
			ValidFrom:    now.Add(-24 * time.Hour),
			ValidUntil:   now.Add(7 * 24 * time.Hour),
			DiscountType: "fixed", DiscountValue: 1000, // 立减 10 元
		},
	}
	if err := db.Create(&batches).Error; err != nil {
		return err
	}
	codes := []models.PromoCode{
		{BatchID: batches[0].ID, Code: "ANNIV-8X4K-29FD-LQ17"},
		{BatchID: batches[0].ID, Code: "ANNIV-7P2M-X8VW-TM63"},
		{BatchID: batches[0].ID, Code: "ANNIV-9WQR-B5HC-KD40"},
		{BatchID: batches[1].ID, Code: "NEWBIE-5K2L-0001"},
		{BatchID: batches[1].ID, Code: "NEWBIE-5K2L-0002"},
		{BatchID: batches[1].ID, Code: "NEWBIE-5K2L-0003"},
	}
	return db.Create(&codes).Error
}
