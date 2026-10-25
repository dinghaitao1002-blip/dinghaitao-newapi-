package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UserModelPrice struct {
	Id        int     `json:"id"`
	UserId    int     `json:"user_id" gorm:"index;not null;uniqueIndex:idx_user_model_price"`
	ModelName string  `json:"model_name" gorm:"type:varchar(191);not null;uniqueIndex:idx_user_model_price"`
	Price     float64 `json:"price" gorm:"not null"`
	Enabled   bool    `json:"enabled" gorm:"not null"`
	CreatedAt int64   `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt int64   `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

func NormalizeUserModelPriceInput(modelName string, price float64) (string, float64, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", 0, errors.New("model_name is required")
	}
	if price < 0 {
		return "", 0, errors.New("price must be greater than or equal to 0")
	}
	return modelName, price, nil
}

func GetUserModelPrices(userId int) ([]UserModelPrice, error) {
	var prices []UserModelPrice
	err := DB.Where("user_id = ?", userId).Order("id desc").Find(&prices).Error
	return prices, err
}

func GetUserModelPrice(userId int, modelName string) (float64, bool) {
	modelName = strings.TrimSpace(modelName)
	if userId <= 0 || modelName == "" {
		return 0, false
	}

	var price UserModelPrice
	tx := DB.Where("user_id = ? AND model_name = ? AND enabled = ?", userId, modelName, true).Limit(1).Find(&price)
	if tx.Error != nil || tx.RowsAffected == 0 {
		return 0, false
	}
	return price.Price, true
}

func UpsertUserModelPrice(userId int, modelName string, price float64, enabled bool) error {
	modelName, price, err := NormalizeUserModelPriceInput(modelName, price)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	item := UserModelPrice{
		UserId:    userId,
		ModelName: modelName,
		Price:     price,
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}

	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "model_name"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"price":      price,
			"enabled":    enabled,
			"updated_at": now,
		}),
	}).Create(&item).Error
}

func DeleteUserModelPrice(userId int, modelName string) error {
	modelName = strings.TrimSpace(modelName)
	if userId <= 0 || modelName == "" {
		return errors.New("invalid user_id or model_name")
	}
	return DB.Where("user_id = ? AND model_name = ?", userId, modelName).Delete(&UserModelPrice{}).Error
}

func UserModelPriceExists(userId int, modelName string) bool {
	var count int64
	DB.Model(&UserModelPrice{}).Where("user_id = ? AND model_name = ?", userId, modelName).Count(&count)
	return count > 0
}

func IsUserModelPriceNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
