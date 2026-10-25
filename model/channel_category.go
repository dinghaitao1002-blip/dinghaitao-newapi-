package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm/clause"
)

type ChannelCategory struct {
	Id          int    `json:"id"`
	Name        string `json:"name" gorm:"type:varchar(64);uniqueIndex;not null"`
	CreatedTime int64  `json:"created_time" gorm:"bigint"`
}

type ChannelCategoryWithCount struct {
	Id           int    `json:"id"`
	Name         string `json:"name"`
	CreatedTime  int64  `json:"created_time"`
	ChannelCount int64  `json:"channel_count"`
}

func ListChannelCategories() ([]ChannelCategoryWithCount, error) {
	var saved []ChannelCategory
	if err := DB.Order("created_time asc, id asc").Find(&saved).Error; err != nil {
		return nil, err
	}

	var assigned []ChannelCategoryWithCount
	if err := DB.Model(&Channel{}).
		Select("TRIM(category) AS name, count(*) AS channel_count").
		Where("category IS NOT NULL AND TRIM(category) != ''").
		Group("TRIM(category)").
		Find(&assigned).Error; err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(assigned))
	for _, item := range assigned {
		counts[item.Name] = item.ChannelCount
	}

	result := make([]ChannelCategoryWithCount, 0, len(saved)+len(assigned))
	seen := make(map[string]struct{}, len(saved))
	for _, category := range saved {
		result = append(result, ChannelCategoryWithCount{
			Id:           category.Id,
			Name:         category.Name,
			CreatedTime:  category.CreatedTime,
			ChannelCount: counts[category.Name],
		})
		seen[category.Name] = struct{}{}
	}
	for _, item := range assigned {
		if _, ok := seen[item.Name]; ok {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}

func EnsureChannelCategory(name string) (*ChannelCategory, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return &ChannelCategory{}, nil
	}
	category := &ChannelCategory{
		Name:        name,
		CreatedTime: common.GetTimestamp(),
	}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(category).Error; err != nil {
		return nil, err
	}
	if err := DB.Where("name = ?", name).First(category).Error; err != nil {
		return nil, err
	}
	return category, nil
}

func AssignChannelsToCategory(ids []int, category string) (int64, error) {
	category = strings.TrimSpace(category)
	if category != "" {
		if _, err := EnsureChannelCategory(category); err != nil {
			return 0, err
		}
	}
	result := DB.Model(&Channel{}).Where("id IN ?", ids).Update("category", category)
	return result.RowsAffected, result.Error
}
