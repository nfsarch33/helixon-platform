package safety

import (
	"sync"
	"time"
)

// TokenCost maps model families to per-token pricing (USD per 1M tokens).
type TokenCost struct {
	InputPer1M  float64
	OutputPer1M float64
}

var defaultPricing = map[string]TokenCost{
	"gpt-4o":          {InputPer1M: 2.50, OutputPer1M: 10.0},
	"gpt-4o-mini":     {InputPer1M: 0.15, OutputPer1M: 0.60},
	"gpt-5":           {InputPer1M: 10.0, OutputPer1M: 30.0},
	"claude-sonnet-4": {InputPer1M: 3.0, OutputPer1M: 15.0},
	"claude-opus-4":   {InputPer1M: 15.0, OutputPer1M: 75.0},
	"qwen3":           {InputPer1M: 0.0, OutputPer1M: 0.0},
	"local":           {InputPer1M: 0.0, OutputPer1M: 0.0},
}

// CostEstimator tracks token usage and estimates cost per session.
type CostEstimator struct {
	mu       sync.Mutex
	pricing  map[string]TokenCost
	sessions map[string]*sessionCost
}

type sessionCost struct {
	model     string
	tokensIn  int
	tokensOut int
	costUSD   float64
	turns     int
	startedAt time.Time
}

// NewCostEstimator creates a cost estimator with default pricing.
func NewCostEstimator() *CostEstimator {
	return &CostEstimator{
		pricing:  defaultPricing,
		sessions: make(map[string]*sessionCost),
	}
}

// SetPricing updates pricing for a model family.
func (c *CostEstimator) SetPricing(model string, cost TokenCost) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pricing[model] = cost
}

// Record logs a turn's token usage for a session.
func (c *CostEstimator) Record(sessionID, model string, tokensIn, tokensOut int) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	sc, ok := c.sessions[sessionID]
	if !ok {
		sc = &sessionCost{model: model, startedAt: time.Now()}
		c.sessions[sessionID] = sc
	}

	sc.tokensIn += tokensIn
	sc.tokensOut += tokensOut
	sc.turns++

	cost := c.estimateCost(model, tokensIn, tokensOut)
	sc.costUSD += cost

	return cost
}

// SessionCost returns the accumulated cost for a session.
func (c *CostEstimator) SessionCost(sessionID string) (tokensIn, tokensOut int, costUSD float64, turns int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sc, ok := c.sessions[sessionID]
	if !ok {
		return 0, 0, 0, 0
	}
	return sc.tokensIn, sc.tokensOut, sc.costUSD, sc.turns
}

// TotalCost returns the total cost across all sessions.
func (c *CostEstimator) TotalCost() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	total := 0.0
	for _, sc := range c.sessions {
		total += sc.costUSD
	}
	return total
}

// EstimateCost calculates the cost for given token counts and model.
func (c *CostEstimator) EstimateCost(model string, tokensIn, tokensOut int) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.estimateCost(model, tokensIn, tokensOut)
}

func (c *CostEstimator) estimateCost(model string, tokensIn, tokensOut int) float64 {
	pricing, ok := c.pricing[model]
	if !ok {
		pricing = defaultPricing["gpt-4o-mini"]
	}
	inCost := float64(tokensIn) * pricing.InputPer1M / 1_000_000
	outCost := float64(tokensOut) * pricing.OutputPer1M / 1_000_000
	return inCost + outCost
}

// ApproximateTokens provides a rough token count estimate from text length.
// Uses the ~4 chars per token heuristic for English text.
func ApproximateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) + 3) / 4
}
