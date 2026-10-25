package model

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ImageQualityLow    = "low"
	ImageQualityMedium = "medium"
	ImageQualityHigh   = "high"

	ImageResolution1K = "1k"
	ImageResolution2K = "2k"
	ImageResolution4K = "4k"
)

var supportedImageQualities = map[string]struct{}{
	ImageQualityLow:    {},
	ImageQualityMedium: {},
	ImageQualityHigh:   {},
}

var supportedImageResolutions = map[string]struct{}{
	ImageResolution1K: {},
	ImageResolution2K: {},
	ImageResolution4K: {},
}

// ModelImageQualityPrice stores public image quality pricing for a model.
type ModelImageQualityPrice struct {
	Id         int     `json:"id"`
	ModelName  string  `json:"model_name" gorm:"type:varchar(191);not null;uniqueIndex:idx_model_image_quality_price"`
	Quality    string  `json:"quality" gorm:"type:varchar(32);not null;uniqueIndex:idx_model_image_quality_price"`
	Resolution string  `json:"resolution" gorm:"type:varchar(32);not null;uniqueIndex:idx_model_image_quality_price"`
	Price      float64 `json:"price" gorm:"not null"`
	Enabled    bool    `json:"enabled" gorm:"not null;default:true"`
	CreatedAt  int64   `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt  int64   `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

// UserModelImageQualityPrice stores per-user private image quality pricing.
type UserModelImageQualityPrice struct {
	Id         int     `json:"id"`
	UserId     int     `json:"user_id" gorm:"index;not null;uniqueIndex:idx_user_model_image_quality_price"`
	ModelName  string  `json:"model_name" gorm:"type:varchar(191);not null;uniqueIndex:idx_user_model_image_quality_price"`
	Quality    string  `json:"quality" gorm:"type:varchar(32);not null;uniqueIndex:idx_user_model_image_quality_price"`
	Resolution string  `json:"resolution" gorm:"type:varchar(32);not null;uniqueIndex:idx_user_model_image_quality_price"`
	Price      float64 `json:"price" gorm:"not null"`
	Enabled    bool    `json:"enabled" gorm:"not null;default:true"`
	CreatedAt  int64   `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt  int64   `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type ImageQualityPriceItem struct {
	Quality    string  `json:"quality"`
	Resolution string  `json:"resolution"`
	Price      float64 `json:"price"`
	Enabled    bool    `json:"enabled"`
}

type ResolvedImagePricingRequest struct {
	Quality    string  `json:"quality"`
	Resolution string  `json:"resolution"`
	ImageCount int     `json:"image_count"`
	RawQuality string  `json:"raw_quality,omitempty"`
	RawSize    string  `json:"raw_size,omitempty"`
	UnitPrice  float64 `json:"unit_price,omitempty"`
	Source     string  `json:"source,omitempty"`
}

func NormalizeImageQuality(raw string) string {
	q := strings.ToLower(strings.TrimSpace(raw))
	switch q {
	case "", "auto", "standard", "normal", "medium", "med", "me":
		return ImageQualityMedium
	case "low", "draft", "basic":
		return ImageQualityLow
	case "high", "hd", "hight", "highest", "ultra":
		return ImageQualityHigh
	default:
		return q
	}
}

func NormalizeImageResolution(rawSize string) string {
	s := strings.ToLower(strings.TrimSpace(rawSize))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "×", "x")

	switch s {
	case "", "auto", "1k", "1024", "1024p":
		return ImageResolution1K
	case "2k", "2048", "2048p":
		return ImageResolution2K
	case "4k", "4096", "4096p":
		return ImageResolution4K
	}

	if strings.Contains(s, "x") {
		parts := strings.SplitN(s, "x", 2)
		if len(parts) == 2 {
			w, errW := strconv.Atoi(parts[0])
			h, errH := strconv.Atoi(parts[1])
			if errW == nil && errH == nil && w > 0 && h > 0 {
				maxSide := w
				if h > maxSide {
					maxSide = h
				}
				switch {
				case maxSide <= 1024:
					return ImageResolution1K
				case maxSide <= 2048:
					return ImageResolution2K
				case maxSide <= 4096:
					return ImageResolution4K
				default:
					return ""
				}
			}
		}
	}

	return s
}

func NormalizeImageCount(n int) int {
	if n <= 0 {
		return 1
	}
	if n > 128 {
		return 128
	}
	return n
}

func ValidateImageQualityResolutionPrice(quality, resolution string, price float64) (string, string, float64, error) {
	quality = NormalizeImageQuality(quality)
	resolution = NormalizeImageResolution(resolution)

	if _, ok := supportedImageQualities[quality]; !ok {
		return "", "", 0, fmt.Errorf("unsupported image quality: %s", quality)
	}
	if _, ok := supportedImageResolutions[resolution]; !ok {
		return "", "", 0, fmt.Errorf("unsupported image resolution: %s", resolution)
	}
	if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return "", "", 0, errors.New("price must be a valid number greater than or equal to 0")
	}
	return quality, resolution, price, nil
}

func NormalizeImageQualityPriceItems(items []ImageQualityPriceItem, requireFullMatrix bool) ([]ImageQualityPriceItem, error) {
	seen := make(map[string]struct{})
	out := make([]ImageQualityPriceItem, 0, len(items))

	for _, item := range items {
		quality, resolution, price, err := ValidateImageQualityResolutionPrice(item.Quality, item.Resolution, item.Price)
		if err != nil {
			return nil, err
		}
		key := quality + ":" + resolution
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("duplicate image quality price item: %s", key)
		}
		seen[key] = struct{}{}
		out = append(out, ImageQualityPriceItem{Quality: quality, Resolution: resolution, Price: price, Enabled: item.Enabled})
	}

	if requireFullMatrix {
		for q := range supportedImageQualities {
			for r := range supportedImageResolutions {
				if _, ok := seen[q+":"+r]; !ok {
					return nil, fmt.Errorf("missing image quality price: %s/%s", q, r)
				}
			}
		}
	}

	return out, nil
}

func GetModelImageQualityPrices(modelName string) ([]ModelImageQualityPrice, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, errors.New("model_name is required")
	}
	var prices []ModelImageQualityPrice
	err := DB.Where("model_name = ?", modelName).Order("quality asc, resolution asc").Find(&prices).Error
	return prices, err
}

func GetModelImageQualityPrice(modelName, quality, resolution string) (float64, bool) {
	modelName = strings.TrimSpace(modelName)
	quality = NormalizeImageQuality(quality)
	resolution = NormalizeImageResolution(resolution)
	if modelName == "" || quality == "" || resolution == "" {
		return 0, false
	}

	var item ModelImageQualityPrice
	tx := DB.Where("model_name = ? AND quality = ? AND resolution = ? AND enabled = ?", modelName, quality, resolution, true).Limit(1).Find(&item)
	if tx.Error != nil || tx.RowsAffected == 0 {
		return 0, false
	}
	return item.Price, true
}

func SaveModelImageQualityPrices(modelName string, items []ImageQualityPriceItem, enabled bool) error {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return errors.New("model_name is required")
	}
	items, err := NormalizeImageQualityPriceItems(items, enabled)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_name = ?", modelName).Delete(&ModelImageQualityPrice{}).Error; err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		rows := make([]ModelImageQualityPrice, 0, len(items))
		for _, item := range items {
			rows = append(rows, ModelImageQualityPrice{ModelName: modelName, Quality: item.Quality, Resolution: item.Resolution, Price: item.Price, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now})
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
}

func DeleteModelImageQualityPrices(modelName string) error {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return errors.New("model_name is required")
	}
	return DB.Where("model_name = ?", modelName).Delete(&ModelImageQualityPrice{}).Error
}

func GetUserModelImageQualityPrices(userId int, modelName string) ([]UserModelImageQualityPrice, error) {
	modelName = strings.TrimSpace(modelName)
	if userId <= 0 {
		return nil, errors.New("invalid user_id")
	}
	query := DB.Where("user_id = ?", userId)
	if modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	var prices []UserModelImageQualityPrice
	err := query.Order("model_name asc, quality asc, resolution asc").Find(&prices).Error
	return prices, err
}

func GetUserModelImageQualityPrice(userId int, modelName, quality, resolution string) (float64, bool) {
	modelName = strings.TrimSpace(modelName)
	quality = NormalizeImageQuality(quality)
	resolution = NormalizeImageResolution(resolution)
	if userId <= 0 || modelName == "" || quality == "" || resolution == "" {
		return 0, false
	}

	var item UserModelImageQualityPrice
	tx := DB.Where("user_id = ? AND model_name = ? AND quality = ? AND resolution = ? AND enabled = ?", userId, modelName, quality, resolution, true).Limit(1).Find(&item)
	if tx.Error != nil || tx.RowsAffected == 0 {
		return 0, false
	}
	return item.Price, true
}

func SaveUserModelImageQualityPrices(userId int, modelName string, items []ImageQualityPriceItem, enabled bool) error {
	modelName = strings.TrimSpace(modelName)
	if userId <= 0 || modelName == "" {
		return errors.New("invalid user_id or model_name")
	}
	items, err := NormalizeImageQualityPriceItems(items, enabled)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND model_name = ?", userId, modelName).Delete(&UserModelImageQualityPrice{}).Error; err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		rows := make([]UserModelImageQualityPrice, 0, len(items))
		for _, item := range items {
			rows = append(rows, UserModelImageQualityPrice{UserId: userId, ModelName: modelName, Quality: item.Quality, Resolution: item.Resolution, Price: item.Price, Enabled: item.Enabled, CreatedAt: now, UpdatedAt: now})
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
}

func DeleteUserModelImageQualityPrices(userId int, modelName string) error {
	modelName = strings.TrimSpace(modelName)
	if userId <= 0 || modelName == "" {
		return errors.New("invalid user_id or model_name")
	}
	return DB.Where("user_id = ? AND model_name = ?", userId, modelName).Delete(&UserModelImageQualityPrice{}).Error
}

func UpsertUserModelImageQualityPrice(userId int, modelName, quality, resolution string, price float64, enabled bool) error {
	modelName = strings.TrimSpace(modelName)
	if userId <= 0 || modelName == "" {
		return errors.New("invalid user_id or model_name")
	}
	quality, resolution, price, err := ValidateImageQualityResolutionPrice(quality, resolution, price)
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	item := UserModelImageQualityPrice{UserId: userId, ModelName: modelName, Quality: quality, Resolution: resolution, Price: price, Enabled: enabled, CreatedAt: now, UpdatedAt: now}

	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "model_name"}, {Name: "quality"}, {Name: "resolution"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"price":      price,
			"enabled":    enabled,
			"updated_at": now,
		}),
	}).Create(&item).Error
}
