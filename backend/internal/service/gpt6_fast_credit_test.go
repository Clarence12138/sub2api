package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func requireGPT6CostRatio(t *testing.T, standard, tier *CostBreakdown, ratio float64) {
	t.Helper()
	for name, pair := range map[string][2]float64{
		"input":       {standard.InputCost, tier.InputCost},
		"output":      {standard.OutputCost, tier.OutputCost},
		"cache-write": {standard.CacheCreationCost, tier.CacheCreationCost},
		"cache-read":  {standard.CacheReadCost, tier.CacheReadCost},
		"total":       {standard.TotalCost, tier.TotalCost},
		"actual":      {standard.ActualCost, tier.ActualCost},
	} {
		require.InDelta(t, pair[0]*ratio, pair[1], 1e-10, name)
	}
}

func TestGPT6FastCredit_DirectPricingAliasesAndTiers(t *testing.T) {
	models := []string{"gpt-6", "gpt-6-astra", "gpt-6-astra-2026-09-01", "gpt-6-astra-preview", "openai/gpt-6", "vendor/openai/gpt-6-astra-2026-09-01", " OPENAI/GPT-6-ASTRA "}
	for _, catalogPriority := range []bool{false, true} {
		for _, model := range models {
			t.Run(fmt.Sprintf("%s/catalog-priority=%t", model, catalogPriority), func(t *testing.T) {
				card := &LiteLLMModelPricing{InputCostPerToken: 10e-6, OutputCostPerToken: 50e-6, CacheCreationInputTokenCost: 12.5e-6, CacheReadInputTokenCost: 1e-6}
				if catalogPriority {
					card.InputCostPerTokenPriority = 20e-6
					card.OutputCostPerTokenPriority = 100e-6
					card.CacheCreationInputTokenCostPriority = 25e-6
					card.CacheReadInputTokenCostPriority = 2e-6
				}
				billing := NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: card}})
				tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheCreationTokens: 200, CacheReadTokens: 300}
				standard, err := billing.CalculateCost(model, tokens, 0.4)
				require.NoError(t, err)
				require.Greater(t, standard.TotalCost, 0.0)
				for tier, ratio := range map[string]float64{"fast": 2.5, "priority": 2.5, " Priority ": 2.5, "": 1, "default": 1, "flex": 0.5, "ultrafast": 2} {
					cost, err := billing.CalculateCostWithServiceTier(model, tokens, 0.4, tier)
					require.NoError(t, err)
					requireGPT6CostRatio(t, standard, cost, ratio)
				}
			})
		}
	}
	for _, model := range []string{"gpt-6-unknown", "gpt-6.1", "gpt-6-astrafoo", "claude-opus-5", "gpt-5.4"} {
		require.False(t, usesOpenAIChatGPTFastCreditMultiplier(model), model)
	}
	for _, model := range []string{"gpt-5.5", "gpt-5.5-pro", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		require.Equal(t, 2.5, serviceTierCostMultiplier("fast", model), model)
	}
}

func TestGPT6FastCredit_UnifiedOverridesAndLongContext(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	resolver := NewModelPricingResolver(nil, billing)
	tokens := UsageTokens{InputTokens: 300000, OutputTokens: 1000, CacheCreationTokens: 200, CacheReadTokens: 300}
	zero, custom := 0.0, 1.7
	for _, override := range []*float64{nil, &zero, &custom} {
		for _, ladder := range []bool{false, true} {
			for _, interval := range []bool{false, true} {
				t.Run(fmt.Sprintf("override=%v/ladder=%t/interval=%t", override, ladder, interval), func(t *testing.T) {
					pricing := &ModelPricing{InputPricePerToken: 10e-6, OutputPricePerToken: 50e-6, CacheCreationPricePerToken: 12.5e-6, CacheReadPricePerToken: 1e-6, InputPricePerTokenPriority: 20e-6, OutputPricePerTokenPriority: 100e-6, FastMultiplier: override, LongContextInputThreshold: 272000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5}
					resolved := &ResolvedPricing{Mode: BillingModeToken, Source: PricingSourceChannel, BasePricing: pricing, longContextPricingEnabled: ladder}
					if interval {
						inputPrice := 30e-6
						resolved.Intervals = []PricingInterval{{MinTokens: 0, InputPrice: &inputPrice}}
					}
					input := CostInput{Ctx: context.Background(), Model: "vendor/gpt-6-astra-2026-09-01", Tokens: tokens, RateMultiplier: 0.4, Resolver: resolver, Resolved: resolved}
					standard, err := billing.CalculateCostUnified(input)
					require.NoError(t, err)
					require.Equal(t, ladder && !interval, standard.LongContextBillingApplied)
					ratio := 2.5
					if override != nil {
						ratio = *override
					}
					for _, tier := range []string{"fast", "priority"} {
						input.ServiceTier = tier
						cost, err := billing.CalculateCostUnified(input)
						require.NoError(t, err)
						requireGPT6CostRatio(t, standard, cost, ratio)
					}
				})
			}
		}
	}
}

func TestGPT6FastCredit_RecordUsageChargesAndPersists(t *testing.T) {
	for _, unified := range []bool{false, true} {
		for _, subscription := range []bool{false, true} {
			for _, freeFast := range []bool{false, true} {
				for _, tier := range []string{"default", "priority", "ultrafast"} {
					for _, ws := range []bool{false, true} {
						t.Run(fmt.Sprintf("unified=%t/sub=%t/free=%t/%s/ws=%t", unified, subscription, freeFast, tier, ws), func(t *testing.T) {
							usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
							billingRepo := &openAIRecordUsageBillingRepoStub{}
							svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
							if unified {
								svc.resolver = NewModelPricingResolver(nil, svc.billingService)
							}
							group := &Group{ID: 77, Platform: PlatformOpenAI, Hydrated: true, Status: StatusActive, RateMultiplier: 0.4, FreeOpenAIFast: freeFast}
							var sub *UserSubscription
							if subscription {
								group.SubscriptionType = SubscriptionTypeSubscription
								sub = &UserSubscription{ID: 99}
							}
							accountRate := 0.8
							input := &OpenAIRecordUsageInput{
								Result: &OpenAIForwardResult{RequestID: "gpt6-credit", Model: "gpt-6-astra", ServiceTier: &tier, UpstreamResponseServiceTier: tier, OpenAIWSMode: ws, Stream: ws, Usage: OpenAIUsage{InputTokens: 1500, OutputTokens: 500, CacheReadInputTokens: 300, CacheCreationInputTokens: 200}, Duration: time.Second},
								APIKey: &APIKey{ID: 2, GroupID: &group.ID, Group: group, Quota: 100}, User: &User{ID: 1},
								Account:      &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: &accountRate, Extra: map[string]any{"quota_limit": 100.0}},
								Subscription: sub, APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{}, EdgeName: "test-edge", EntryHost: "test.invalid",
							}
							if ws {
								input.Account.Type = AccountTypeOAuth
							}
							require.NoError(t, svc.RecordUsage(context.Background(), input))
							require.Equal(t, 1, billingRepo.calls)
							require.Equal(t, 1, usageRepo.calls)
							log := usageRepo.lastLog
							require.NotNil(t, log)
							ratio := 1.0
							switch tier {
							case "priority":
								ratio = 2.5
							case "ultrafast":
								ratio = 2
							}
							standard := 1000*10e-6 + 500*50e-6 + 200*12.5e-6 + 300*1e-6
							expectedTotal := standard * ratio
							expectedActual := expectedTotal * 0.4
							if freeFast && tier == "priority" {
								expectedActual = standard * 0.4
							}
							require.InDelta(t, expectedTotal, log.TotalCost, 1e-10)
							require.InDelta(t, expectedActual, log.ActualCost, 1e-10)
							require.InDelta(t, 1000*10e-6*ratio, log.InputCost, 1e-10)
							require.InDelta(t, 500*50e-6*ratio, log.OutputCost, 1e-10)
							require.InDelta(t, 200*12.5e-6*ratio, log.CacheCreationCost, 1e-10)
							require.InDelta(t, 300*1e-6*ratio, log.CacheReadCost, 1e-10)
							require.Equal(t, tier, *log.ServiceTier)
							require.Equal(t, "test-edge", *log.EdgeName)
							require.Equal(t, "test.invalid", *log.EntryHost)
							cmd := billingRepo.lastCmd
							require.InDelta(t, expectedActual, cmd.APIKeyQuotaCost, 1e-8)
							if subscription {
								require.InDelta(t, expectedActual, cmd.SubscriptionCost, 1e-8)
								require.Zero(t, cmd.BalanceCost)
							} else {
								require.InDelta(t, expectedActual, cmd.BalanceCost, 1e-8)
								require.Zero(t, cmd.SubscriptionCost)
							}
							if !ws {
								require.InDelta(t, expectedTotal*accountRate, cmd.AccountQuotaCost, 1e-8)
							}
						})
					}
				}
			}
		}
	}
}
