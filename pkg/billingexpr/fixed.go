package billingexpr

import (
	"fmt"
	"math"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// ImageUnitPrice recognizes a single, unconditional USD price per image. It
// deliberately inspects the original tree so optimized-away branches and
// request multipliers cannot turn dynamic prices into private-price candidates.
// Task expressions return USD directly; ordinary expressions use fixed().
func ImageUnitPrice(expression string, taskUsage bool) (float64, bool) {
	_, body := ParseExprVersion(expression)
	tree, err := parser.Parse(body)
	if err != nil {
		return 0, false
	}
	node := tree.Node
	if taskUsage {
		if call, ok := node.(*ast.CallNode); ok {
			callee, named := call.Callee.(*ast.IdentifierNode)
			if !named || callee.Value != "tier" || len(call.Arguments) != 2 {
				return 0, false
			}
			if _, literal := call.Arguments[0].(*ast.StringNode); !literal {
				return 0, false
			}
			node = call.Arguments[1]
		}
	}
	product, ok := node.(*ast.BinaryNode)
	if !ok || product.Operator != "*" {
		return 0, false
	}
	left, right := product.Left, product.Right
	if taskUsage {
		price, numeric := requestRuleNumber(right)
		if !numeric {
			left, right = right, left
			price, numeric = requestRuleNumber(right)
		}
		usage, call := left.(*ast.CallNode)
		if !numeric || !call || len(usage.Arguments) != 1 {
			return 0, false
		}
		callee, named := usage.Callee.(*ast.IdentifierNode)
		key, literal := usage.Arguments[0].(*ast.StringNode)
		if !named || callee.Value != "u" || !literal || key.Value != "image_count" || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return 0, false
		}
		return price, true
	}
	quantity, identifier := right.(*ast.IdentifierNode)
	if !identifier || quantity.Value != "image_count" {
		left, right = right, left
		quantity, identifier = right.(*ast.IdentifierNode)
	}
	if !identifier || quantity.Value != "image_count" {
		return 0, false
	}
	tier, call := left.(*ast.CallNode)
	if !call || len(tier.Arguments) != 2 {
		return 0, false
	}
	callee, named := tier.Callee.(*ast.IdentifierNode)
	_, literal := tier.Arguments[0].(*ast.StringNode)
	if !named || callee.Value != "tier" || !literal {
		return 0, false
	}
	fixed, call := tier.Arguments[1].(*ast.CallNode)
	if !call || len(fixed.Arguments) != 1 {
		return 0, false
	}
	callee, named = fixed.Callee.(*ast.IdentifierNode)
	price, numeric := requestRuleNumber(fixed.Arguments[0])
	if !named || callee.Value != "fixed" || !numeric || price < 0 || math.IsNaN(price) || math.IsInf(price*1_000_000, 0) {
		return 0, false
	}
	return price, true
}

// UsesFixedPricing includes unselected branches, even when compilation later
// optimizes them away. Hosts use it to reject unsupported billing entrances.
func UsesFixedPricing(expression string) bool {
	return UsesFixedPricingByHash(expression, ExprHashString(expression))
}

// UsesFixedPricingByHash avoids hashing an expression already in a snapshot.
func UsesFixedPricingByHash(expression, hash string) bool {
	entry, err := compileEntryFromCacheByHash(expression, hash)
	return err == nil && entry.fixedPricing
}

func containsPricingMarker(node ast.Node) bool {
	return ast.Find(node, func(part ast.Node) bool {
		identifier, ok := part.(*ast.IdentifierNode)
		return ok && (identifier.Value == "tier" || identifier.Value == "fixed")
	}) != nil
}

func isRequestPriceMultiplier(node ast.Node) bool {
	if identifier, ok := node.(*ast.IdentifierNode); ok && identifier.Value == "image_count" {
		return true
	}
	conditional, ok := node.(*ast.ConditionalNode)
	if !ok || !usesRequestProbe(conditional.Cond) || containsPricingMarker(conditional.Cond) {
		return false
	}
	multiplier, multiplierOK := requestRuleNumber(conditional.Exp1)
	fallback, fallbackOK := requestRuleNumber(conditional.Exp2)
	return multiplierOK && fallbackOK && fallback == 1 && multiplier >= 0 && !math.IsNaN(multiplier) && !math.IsInf(multiplier, 0)
}

// validateFixedPricingTree enforces one pricing leaf per execution. Without
// this invariant, adding two tiers or multiplying a fixed price by tokens
// would make both the request charge and its billing-unit trace ambiguous.
func validateFixedPricingTree(node ast.Node) error {
	switch part := node.(type) {
	case *ast.ConditionalNode:
		if containsPricingMarker(part.Cond) {
			break
		}
		if err := validateFixedPricingTree(part.Exp1); err != nil {
			return err
		}
		return validateFixedPricingTree(part.Exp2)
	case *ast.BinaryNode:
		if part.Operator != "*" {
			break
		}
		if isRequestPriceMultiplier(part.Right) {
			return validateFixedPricingTree(part.Left)
		}
		if isRequestPriceMultiplier(part.Left) {
			return validateFixedPricingTree(part.Right)
		}
	case *ast.CallNode:
		callee, ok := part.Callee.(*ast.IdentifierNode)
		if !ok || callee.Value != "tier" || len(part.Arguments) != 2 || containsPricingMarker(part.Arguments[0]) {
			break
		}
		price := part.Arguments[1]
		fixed, ok := price.(*ast.CallNode)
		if ok {
			function, direct := fixed.Callee.(*ast.IdentifierNode)
			if direct && function.Value == "fixed" && len(fixed.Arguments) == 1 {
				amount, literal := requestRuleNumber(fixed.Arguments[0])
				if literal && amount >= 0 && !math.IsNaN(amount) && !math.IsInf(amount*1_000_000, 0) {
					return nil
				}
				return fmt.Errorf("fixed price must be a finite, non-negative numeric literal with a finite v1 value")
			}
		}
		if !containsPricingMarker(price) {
			return nil
		}
	}
	return fmt.Errorf("fixed pricing requires tier(name, fixed(amount)) leaves, conditional tiers and request multipliers; token and fixed charges cannot be combined in one leaf")
}
