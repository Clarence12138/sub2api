package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

type OpenAIQuotaSubscriptionSyncService struct {
	settingRepo         SettingRepository
	accountRepo         AccountRepository
	groupRepo           GroupRepository
	userSubRepo         UserSubscriptionRepository
	subscriptionService *SubscriptionService
	quotaService        OpenAIQuotaUsageQuerier
	idempotencyRepo     IdempotencyRepository
	lockCache           LeaderLockCache
	db                  *sql.DB

	instanceID string
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

func NewOpenAIQuotaSubscriptionSyncService(
	settingRepo SettingRepository,
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	userSubRepo UserSubscriptionRepository,
	subscriptionService *SubscriptionService,
	quotaService OpenAIQuotaUsageQuerier,
	idempotencyRepo IdempotencyRepository,
) *OpenAIQuotaSubscriptionSyncService {
	return &OpenAIQuotaSubscriptionSyncService{
		settingRepo:         settingRepo,
		accountRepo:         accountRepo,
		groupRepo:           groupRepo,
		userSubRepo:         userSubRepo,
		subscriptionService: subscriptionService,
		quotaService:        quotaService,
		idempotencyRepo:     idempotencyRepo,
		instanceID:          "openai-quota-sub-sync-" + time.Now().UTC().Format("20060102150405.000000000"),
		stopCh:              make(chan struct{}),
	}
}

func ProvideOpenAIQuotaSubscriptionSyncService(
	settingRepo SettingRepository,
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	userSubRepo UserSubscriptionRepository,
	subscriptionService *SubscriptionService,
	quotaService *OpenAIQuotaService,
	idempotencyRepo IdempotencyRepository,
	lockCache LeaderLockCache,
	db *sql.DB,
) *OpenAIQuotaSubscriptionSyncService {
	svc := NewOpenAIQuotaSubscriptionSyncService(
		settingRepo,
		accountRepo,
		groupRepo,
		userSubRepo,
		subscriptionService,
		quotaService,
		idempotencyRepo,
	)
	svc.SetLeaderLockDeps(lockCache, db)
	svc.Start()
	return svc
}

func (s *OpenAIQuotaSubscriptionSyncService) SetLeaderLockDeps(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

func (s *OpenAIQuotaSubscriptionSyncService) Start() {
	if s == nil {
		return
	}
	s.wg.Add(1)
	go s.loop()
}

func (s *OpenAIQuotaSubscriptionSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		s.wg.Wait()
	})
}

func (s *OpenAIQuotaSubscriptionSyncService) loop() {
	defer s.wg.Done()
	for {
		interval := s.runConfiguredCycle(context.Background())
		timer := time.NewTimer(interval)
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *OpenAIQuotaSubscriptionSyncService) runConfiguredCycle(ctx context.Context) time.Duration {
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		slog.Error("openai quota subscription sync: load config failed", "error", err)
		return time.Duration(defaultOpenAIQuotaSubscriptionSyncPollIntervalSeconds) * time.Second
	}
	interval := pollIntervalDuration(cfg.PollIntervalSeconds)
	if !cfg.Enabled || len(enabledOpenAIQuotaSyncRules(cfg.Rules)) == 0 {
		return interval
	}
	release, acquired := tryAcquireSingletonLeaderLock(
		ctx,
		s.lockCache,
		s.db,
		openAIQuotaSubscriptionSyncLeaderLockKey,
		s.instanceID,
		leaderLockTTL(interval),
	)
	if !acquired {
		return interval
	}
	defer release()
	if err := s.ProcessOnce(ctx); err != nil {
		slog.Error("openai quota subscription sync: poll failed", "error", err)
	}
	return interval
}

func pollIntervalDuration(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = defaultOpenAIQuotaSubscriptionSyncPollIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

func leaderLockTTL(interval time.Duration) time.Duration {
	ttl := interval * 2
	if ttl < time.Minute {
		return time.Minute
	}
	return ttl
}

func enabledOpenAIQuotaSyncRules(rules []OpenAIQuotaSubscriptionSyncRule) []OpenAIQuotaSubscriptionSyncRule {
	enabled := make([]OpenAIQuotaSubscriptionSyncRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			enabled = append(enabled, rule)
		}
	}
	return enabled
}

func openAIQuotaSyncListParams(page int) pagination.PaginationParams {
	return pagination.PaginationParams{Page: page, PageSize: 200}
}
