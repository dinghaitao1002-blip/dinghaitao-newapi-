package billing_setting

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

const GroupModelPricingOption = "billing_setting.group_model_pricing"

// GroupModelPricingRule keeps the expression as the only stored price. Unit
// describes its billing quantity and does not add another conversion factor.
type GroupModelPricingRule struct {
	Unit       string `json:"unit"`
	Expression string `json:"expression"`
	Enabled    bool   `json:"enabled"`
}

// GroupModelPricingValues are derived from a validated template, never stored.
// Token prices use USD per million tokens; all other prices use USD per unit.
type GroupModelPricingValues struct {
	UnitPrice   float64
	InputPrice  float64
	OutputPrice float64
}

const groupPriceNumber = `([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)`

var groupPricingTemplates = map[string]*regexp.Regexp{
	"request": regexp.MustCompile(`^\s*tier\s*\(\s*"group_model"\s*,\s*fixed\s*\(\s*` + groupPriceNumber + `\s*\)\s*\)\s*$`),
	"image":   regexp.MustCompile(`^\s*tier\s*\(\s*"group_model"\s*,\s*fixed\s*\(\s*` + groupPriceNumber + `\s*\)\s*\)\s*\*\s*image_count\s*$`),
	"second":  regexp.MustCompile(`^\s*tier\s*\(\s*"group_model"\s*,\s*u\s*\(\s*"seconds"\s*\)\s*\*\s*` + groupPriceNumber + `\s*\)\s*$`),
	"token":   regexp.MustCompile(`^\s*tier\s*\(\s*"group_model"\s*,\s*p\s*\*\s*` + groupPriceNumber + `\s*\+\s*c\s*\*\s*` + groupPriceNumber + `\s*\)\s*$`),
}

// ParseGroupModelPricingRule accepts only the four administrator-editable
// templates. Full matching also rejects unreachable branches and extra charges.
func ParseGroupModelPricingRule(rule GroupModelPricingRule) (GroupModelPricingValues, error) {
	template, supported := groupPricingTemplates[rule.Unit]
	if !supported {
		return GroupModelPricingValues{}, fmt.Errorf("group model pricing unit must be request, image, second, or token")
	}
	match := template.FindStringSubmatch(rule.Expression)
	if match == nil {
		return GroupModelPricingValues{}, fmt.Errorf("group model pricing expression does not match the %s template", rule.Unit)
	}
	prices := make([]float64, len(match)-1)
	for index, text := range match[1:] {
		price, err := strconv.ParseFloat(text, 64)
		if err != nil || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return GroupModelPricingValues{}, fmt.Errorf("group model prices must be finite and non-negative")
		}
		prices[index] = price
	}
	if _, err := billingexpr.CompileFromCache(rule.Expression); err != nil {
		return GroupModelPricingValues{}, fmt.Errorf("invalid group model pricing expression: %w", err)
	}
	if rule.Unit == "token" {
		return GroupModelPricingValues{InputPrice: prices[0], OutputPrice: prices[1]}, nil
	}
	return GroupModelPricingValues{UnitPrice: prices[0]}, nil
}

// ValidateGroupModelPricingJSONString validates replacements before persistence
// and before startup publication. Disabled rules retain the same schema.
func ValidateGroupModelPricingJSONString(raw string) error {
	var groups map[string]map[string]common.RawMessage
	if err := common.UnmarshalJsonStr(raw, &groups); err != nil || groups == nil {
		return fmt.Errorf("group model pricing must be a JSON object mapping groups to model rules")
	}
	for group, models := range groups {
		if group == "" || group != strings.TrimSpace(group) || models == nil {
			return fmt.Errorf("group model pricing requires non-empty group names and model objects")
		}
		for modelName, encoded := range models {
			if modelName == "" || modelName != strings.TrimSpace(modelName) {
				return fmt.Errorf("group model pricing requires non-empty model names")
			}
			var fields map[string]common.RawMessage
			if err := common.Unmarshal(encoded, &fields); err != nil || len(fields) != 3 {
				return fmt.Errorf("group %s model %s must contain only unit, expression, and enabled", group, modelName)
			}
			for _, name := range []string{"unit", "expression", "enabled"} {
				if _, present := fields[name]; !present || string(fields[name]) == "null" {
					return fmt.Errorf("group %s model %s requires %s", group, modelName, name)
				}
			}
			var rule GroupModelPricingRule
			if err := common.Unmarshal(encoded, &rule); err != nil {
				return fmt.Errorf("invalid group %s model %s pricing rule: %w", group, modelName, err)
			}
			if _, err := ParseGroupModelPricingRule(rule); err != nil {
				return fmt.Errorf("group %s model %s: %w", group, modelName, err)
			}
		}
	}
	return nil
}

// GetGroupModelPricingRule returns an enabled exact match. In particular,
// explicit zero prices remain configured; disabled or removed rules fall back.
func GetGroupModelPricingRule(group, modelName string) (GroupModelPricingRule, bool) {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	rule, exists := billingSetting.GroupModelPricing[group][modelName]
	return rule, exists && rule.Enabled
}

// GetGroupModelPricingCopy returns an independent administration snapshot.
// Callers exposing it publicly must filter groups and disabled rules first.
func GetGroupModelPricingCopy() map[string]map[string]GroupModelPricingRule {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	groups := make(map[string]map[string]GroupModelPricingRule, len(billingSetting.GroupModelPricing))
	for group, rules := range billingSetting.GroupModelPricing {
		groups[group] = maps.Clone(rules)
	}
	return groups
}
