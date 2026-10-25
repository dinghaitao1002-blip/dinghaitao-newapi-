package controller

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const maxChannelCategoryNameLength = 64

type channelCategoryRequest struct {
	Name string `json:"name"`
}

type assignChannelCategoryRequest struct {
	Ids      []int  `json:"ids"`
	Category string `json:"category"`
}

func normalizeChannelCategoryName(name string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "渠道分类名称不能为空"
	}
	if utf8.RuneCountInString(name) > maxChannelCategoryNameLength {
		return "", "渠道分类不能超过 64 个字符"
	}
	if strings.EqualFold(name, "all") || name == uncategorizedChannelFilter {
		return "", "该名称为系统保留名称"
	}
	return name, ""
}

func GetChannelCategories(c *gin.Context) {
	categories, err := model.ListChannelCategories()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, categories)
}

func CreateChannelCategory(c *gin.Context) {
	request := channelCategoryRequest{}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	name, message := normalizeChannelCategoryName(request.Name)
	if message != "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": message})
		return
	}
	category, err := model.EnsureChannelCategory(name)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "channel.category_create", map[string]interface{}{"name": name})
	common.ApiSuccess(c, category)
}

func AssignChannelCategory(c *gin.Context) {
	request := assignChannelCategoryRequest{}
	if err := c.ShouldBindJSON(&request); err != nil || len(request.Ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "请选择至少一个渠道"})
		return
	}
	category := strings.TrimSpace(request.Category)
	if category != "" {
		var message string
		category, message = normalizeChannelCategoryName(category)
		if message != "" {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": message})
			return
		}
	}
	count, err := model.AssignChannelsToCategory(request.Ids, category)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "channel.category_assign", map[string]interface{}{
		"ids": request.Ids, "category": category, "count": count,
	})
	common.ApiSuccess(c, gin.H{"updated": count})
}
