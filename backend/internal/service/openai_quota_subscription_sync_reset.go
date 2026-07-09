package service

import (
	"context"
	"fmt"
	"time"
)

func (s *OpenAIQuotaSubscriptionSyncService) resetTargetGroupSubscriptions(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	windowStart time.Time,
) (int, error) {
	count := 0
	now := time.Now().UTC()
	for _, groupID := range rule.TargetGroupIDs {
		groupCount, err := s.resetGroupSubscriptions(ctx, rule, groupID, windowStart.UTC(), now)
		count += groupCount
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *OpenAIQuotaSubscriptionSyncService) resetGroupSubscriptions(
	ctx context.Context,
	rule OpenAIQuotaSubscriptionSyncRule,
	groupID int64,
	windowStart time.Time,
	now time.Time,
) (int, error) {
	count := 0
	for page := 1; ; page++ {
		subs, result, err := s.userSubRepo.ListByGroupID(ctx, groupID, openAIQuotaSyncListParams(page))
		if err != nil {
			return count, fmt.Errorf("list subscriptions for group %d: %w", groupID, err)
		}
		for i := range subs {
			if !isOpenAIQuotaSyncEligibleSubscription(subs[i], now) {
				continue
			}
			if _, err := s.subscriptionService.ResetQuotaAt(
				ctx,
				subs[i].ID,
				rule.ResetDaily,
				rule.ResetWeekly,
				rule.ResetMonthly,
				windowStart,
			); err != nil {
				return count, fmt.Errorf("reset subscription %d: %w", subs[i].ID, err)
			}
			count++
		}
		if result == nil || result.Page >= result.Pages {
			return count, nil
		}
	}
}

func isOpenAIQuotaSyncEligibleSubscription(sub UserSubscription, now time.Time) bool {
	return sub.Status == SubscriptionStatusActive && sub.ExpiresAt.After(now)
}
