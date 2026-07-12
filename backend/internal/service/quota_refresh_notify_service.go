package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/google/uuid"
)

const (
	quotaRefreshNotifyLeaderLockKey = "quota_refresh_notify:leader"
	// leader lock TTL 与单轮预算之间保留安全余量，避免租约到期时仍在处理账号。
	quotaRefreshNotifyLeaderLockTTL = 3 * time.Minute
	quotaRefreshNotifyLockSafety    = 15 * time.Second
	quotaRefreshNotifyCycleTimeout  = quotaRefreshNotifyLeaderLockTTL - quotaRefreshNotifyLockSafety
	// 默认轮询间隔
	quotaRefreshNotifyDefaultInterval = 2 * time.Minute
	// 同一账号发信防抖
	quotaRefreshNotifyDebounce = 15 * time.Minute
	// 账号间请求抖动上限
	quotaRefreshNotifyMaxJitter = 400 * time.Millisecond
	// 单账号探测超时
	quotaRefreshNotifyProbeTimeout = 25 * time.Second
	// resets_at 至少前移多久才视为新窗口
	quotaRefreshResetsAtMinAdvance = time.Minute
	// 用量下降阈值（百分点）
	quotaRefreshUtilDropMin         = 5.0
	quotaRefreshUtilDropSignificant = 30.0
	quotaRefreshUtilHigh            = 80.0
	quotaRefreshUtilLow             = 30.0
)

// usageWindowSnap 单个用量窗口快照
type usageWindowSnap struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

// quotaRefreshSnapshot 账号级快照
type quotaRefreshSnapshot struct {
	SampledAt string                     `json:"sampled_at"`
	Windows   map[string]usageWindowSnap `json:"windows"`
}

// refreshedWindow 检测出的已刷新窗口
type refreshedWindow struct {
	Key         string     `json:"key"`
	OldUtil     float64    `json:"old_util"`
	NewUtil     float64    `json:"new_util"`
	OldResetsAt *time.Time `json:"old_resets_at,omitempty"`
	NewResetsAt *time.Time `json:"new_resets_at,omitempty"`
}

type quotaRefreshPendingNotification struct {
	EventKey          string               `json:"event_key"`
	Snapshot          quotaRefreshSnapshot `json:"snapshot"`
	Windows           []refreshedWindow    `json:"windows"`
	Delivered         []string             `json:"delivered,omitempty"`
	LastNotifiedByKey map[string]string    `json:"last_notified_by_key,omitempty"`
	CreatedAt         string               `json:"created_at"`
}

// quotaRefreshAccountRepo 账号读写接口
type quotaRefreshAccountRepo interface {
	FindByExtraField(ctx context.Context, key string, value any) ([]Account, error)
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

// quotaRefreshUsageReader 上游用量查询接口
type quotaRefreshUsageReader interface {
	GetUsage(ctx context.Context, accountID int64, force ...bool) (*UsageInfo, error)
}

// quotaRefreshUserRepo 管理员邮箱查询接口
type quotaRefreshUserRepo interface {
	ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error)
}

// quotaRefreshSettingRepo 站点名等设置
type quotaRefreshSettingRepo interface {
	GetValue(ctx context.Context, key string) (string, error)
}

type quotaRefreshEmailSender interface {
	SendEmail(ctx context.Context, to, subject, htmlBody string) error
}

type quotaRefreshNotificationSender interface {
	Send(ctx context.Context, input NotificationEmailSendInput) error
}

// QuotaRefreshNotifyService 定期探测关注账号的上游用量窗口，
// 在窗口重置（额度恢复）时通过 SMTP 通知管理员主邮箱。
type QuotaRefreshNotifyService struct {
	accountRepo              quotaRefreshAccountRepo
	usageReader              quotaRefreshUsageReader
	userRepo                 quotaRefreshUserRepo
	settingRepo              quotaRefreshSettingRepo
	emailService             quotaRefreshEmailSender
	notificationEmailService quotaRefreshNotificationSender

	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	lockCache  LeaderLockCache
	db         *sql.DB
	instanceID string
	cursorMu   sync.Mutex
	nextCursor int
}

// NewQuotaRefreshNotifyService 创建额度刷新通知服务。
func NewQuotaRefreshNotifyService(
	accountRepo quotaRefreshAccountRepo,
	usageReader quotaRefreshUsageReader,
	userRepo quotaRefreshUserRepo,
	settingRepo quotaRefreshSettingRepo,
	emailService quotaRefreshEmailSender,
	interval time.Duration,
) *QuotaRefreshNotifyService {
	if interval <= 0 {
		interval = quotaRefreshNotifyDefaultInterval
	}
	return &QuotaRefreshNotifyService{
		accountRepo:  accountRepo,
		usageReader:  usageReader,
		userRepo:     userRepo,
		settingRepo:  settingRepo,
		emailService: emailService,
		interval:     interval,
		stopCh:       make(chan struct{}),
		instanceID:   uuid.NewString(),
	}
}

// SetLeaderLock 注入多实例 leader lock。
func (s *QuotaRefreshNotifyService) SetLeaderLock(lockCache LeaderLockCache, db *sql.DB) {
	if s == nil {
		return
	}
	s.lockCache = lockCache
	s.db = db
}

// SetNotificationEmailService 注入模板化邮件服务。
func (s *QuotaRefreshNotifyService) SetNotificationEmailService(svc quotaRefreshNotificationSender) {
	if s == nil {
		return
	}
	s.notificationEmailService = svc
}

// Start 启动后台轮询。
func (s *QuotaRefreshNotifyService) Start() {
	if s == nil || s.accountRepo == nil || s.usageReader == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop 停止后台轮询。
func (s *QuotaRefreshNotifyService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *QuotaRefreshNotifyService) runOnce() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), quotaRefreshNotifyCycleTimeout)
	defer cancel()

	release, ok := tryAcquireSingletonLeaderLock(ctx, s.lockCache, s.db, quotaRefreshNotifyLeaderLockKey, s.instanceID, quotaRefreshNotifyLeaderLockTTL)
	if !ok {
		return
	}
	defer release()

	accounts, err := s.accountRepo.FindByExtraField(ctx, extraKeyQuotaRefreshNotifyEnabled, true)
	if err != nil {
		slog.Warn("quota_refresh_notify: list watched accounts failed", "error", err)
		return
	}
	if len(accounts) == 0 {
		return
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	accounts, start := s.rotateAccounts(accounts)

	recipients := s.listAdminEmails(ctx)
	if len(recipients) == 0 {
		slog.Warn("quota_refresh_notify: no admin emails found; skip cycle")
		return
	}
	siteName := s.getSiteName(ctx)

	processed := 0
	defer func() { s.advanceCursor(start, processed, len(accounts)) }()
	for i := range accounts {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-s.stopCh:
			return
		default:
		}
		// 小抖动，避免瞬时打爆上游
		if i > 0 {
			jitter := time.Duration(rand.Intn(int(quotaRefreshNotifyMaxJitter)))
			select {
			case <-time.After(jitter):
			case <-s.stopCh:
				return
			}
		}
		s.processAccount(ctx, &accounts[i], recipients, siteName)
		processed++
	}
}

func (s *QuotaRefreshNotifyService) rotateAccounts(accounts []Account) ([]Account, int) {
	if len(accounts) == 0 {
		return accounts, 0
	}
	s.cursorMu.Lock()
	start := s.nextCursor % len(accounts)
	s.cursorMu.Unlock()
	rotated := append([]Account(nil), accounts[start:]...)
	rotated = append(rotated, accounts[:start]...)
	return rotated, start
}

func (s *QuotaRefreshNotifyService) advanceCursor(start, processed, total int) {
	if total == 0 {
		return
	}
	s.cursorMu.Lock()
	s.nextCursor = (start + processed) % total
	s.cursorMu.Unlock()
}

func (s *QuotaRefreshNotifyService) processAccount(ctx context.Context, account *Account, recipients []string, siteName string) {
	if account == nil || !account.GetQuotaRefreshNotifyEnabled() {
		return
	}
	// 仅处理 active 账号
	if account.Status != "" && account.Status != StatusActive {
		return
	}
	if pending := parseQuotaRefreshPending(account.Extra); pending != nil {
		s.deliverPending(ctx, account, pending, recipients, siteName)
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, quotaRefreshNotifyProbeTimeout)
	defer cancel()

	usage, err := s.usageReader.GetUsage(probeCtx, account.ID)
	if err != nil {
		slog.Debug("quota_refresh_notify: get usage failed", "account_id", account.ID, "error", err)
		return
	}
	if usage == nil {
		return
	}

	newWindows := extractUsageWindows(usage)
	if len(newWindows) == 0 {
		return
	}

	oldSnap := parseQuotaRefreshSnapshot(account.Extra)
	newSnap := quotaRefreshSnapshot{
		SampledAt: time.Now().UTC().Format(time.RFC3339),
		Windows:   newWindows,
	}
	// 冷启动：仅写 snapshot，不发信
	if oldSnap == nil || len(oldSnap.Windows) == 0 {
		if err := s.persistSnapshot(ctx, account.ID, newSnap, nil, nil); err != nil {
			slog.Warn("quota_refresh_notify: persist initial snapshot failed", "account_id", account.ID, "error", err)
		}
		return
	}

	now := time.Now().UTC()
	refreshed := detectQuotaRefresh(oldSnap.Windows, newWindows, now)
	refreshed = filterRefreshedBySelectedWindows(refreshed, account.GetQuotaRefreshNotifyWindows())
	lastNotifiedByKey := parseWindowNotifiedAt(account.Extra)
	refreshed = filterDebouncedWindows(refreshed, lastNotifiedByKey, now)
	if len(refreshed) == 0 {
		if err := s.persistSnapshot(ctx, account.ID, newSnap, nil, nil); err != nil {
			slog.Warn("quota_refresh_notify: persist snapshot failed", "account_id", account.ID, "error", err)
		}
		return
	}

	pending := newQuotaRefreshPending(account.ID, refreshed, newSnap, now)
	pending.LastNotifiedByKey = formatWindowNotifiedAt(lastNotifiedByKey)
	if err := s.persistPending(ctx, account.ID, pending); err != nil {
		slog.Warn("quota_refresh_notify: persist pending event failed", "account_id", account.ID, "error", err)
		return
	}
	s.deliverPending(ctx, account, pending, recipients, siteName)
}

func (s *QuotaRefreshNotifyService) persistSnapshot(ctx context.Context, accountID int64, snap quotaRefreshSnapshot, notifiedAt *time.Time, windowTimes map[string]string) error {
	if s.accountRepo == nil {
		return errors.New("quota refresh account repository is not configured")
	}
	updates := map[string]any{
		extraKeyQuotaRefreshNotifySnapshot: snapshotToMap(snap),
	}
	if notifiedAt != nil {
		updates[extraKeyQuotaRefreshLastNotifiedAt] = notifiedAt.UTC().Format(time.RFC3339)
		updates[extraKeyQuotaRefreshLastNotifiedWindows] = windowTimes
		updates[extraKeyQuotaRefreshPending] = nil
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, updates)
}

func (s *QuotaRefreshNotifyService) persistPending(ctx context.Context, accountID int64, pending *quotaRefreshPendingNotification) error {
	if s.accountRepo == nil {
		return errors.New("quota refresh account repository is not configured")
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{extraKeyQuotaRefreshPending: pending})
}

func (s *QuotaRefreshNotifyService) deliverPending(ctx context.Context, account *Account, pending *quotaRefreshPendingNotification, recipients []string, siteName string) {
	if account == nil || pending == nil {
		return
	}
	delivered := make(map[string]bool, len(pending.Delivered))
	for _, email := range pending.Delivered {
		delivered[strings.ToLower(email)] = true
	}
	var deliveryFailed bool
	for _, recipient := range recipients {
		key := strings.ToLower(recipient)
		if delivered[key] {
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, emailSendTimeout)
		err := s.sendQuotaRefreshEmail(sendCtx, recipient, account, pending, siteName)
		cancel()
		if err != nil {
			deliveryFailed = true
			slog.Warn("quota_refresh_notify: delivery failed", "to", recipient, "account_id", account.ID, "error", err)
			continue
		}
		pending.Delivered = append(pending.Delivered, recipient)
		delivered[key] = true
		if err := s.persistPending(ctx, account.ID, pending); err != nil {
			slog.Warn("quota_refresh_notify: persist recipient delivery failed", "account_id", account.ID, "error", err)
			return
		}
	}
	if deliveryFailed {
		return
	}
	now := time.Now().UTC()
	windowTimes := cloneStringMap(pending.LastNotifiedByKey)
	for _, window := range pending.Windows {
		windowTimes[window.Key] = now.Format(time.RFC3339)
	}
	if err := s.persistSnapshot(ctx, account.ID, pending.Snapshot, &now, windowTimes); err != nil {
		slog.Warn("quota_refresh_notify: finalize delivery failed", "account_id", account.ID, "error", err)
	}
}

func (s *QuotaRefreshNotifyService) sendQuotaRefreshEmail(ctx context.Context, to string, account *Account, pending *quotaRefreshPendingNotification, siteName string) error {
	if s.emailService == nil && s.notificationEmailService == nil {
		return errors.New("email service is not configured")
	}
	if siteName == "" {
		siteName = defaultSiteName
	}
	summary := formatWindowsSummary(pending.Windows)
	summaryHTML := formatWindowsSummaryHTML(pending.Windows)
	if s.notificationEmailService != nil {
		err := s.notificationEmailService.Send(ctx, NotificationEmailSendInput{
			Event: NotificationEmailEventAccountQuotaRefresh, RecipientEmail: to,
			RecipientName: emailRecipientName(to), SourceType: "account_quota_refresh",
			SourceID: strconv.FormatInt(account.ID, 10), ReminderKey: pending.EventKey,
			Variables: map[string]string{
				"account_id": strconv.FormatInt(account.ID, 10), "account_name": account.Name,
				"platform": account.Platform, "windows_summary": summary,
			},
			RawHTMLVariables: map[string]string{"windows_html": summaryHTML},
		})
		if err == nil || !shouldFallbackNotificationEmail(err) {
			return err
		}
		slog.Warn("quota_refresh_notify: template send failed; fallback", "to", to, "account_id", account.ID, "error", err)
	}
	if s.emailService == nil {
		return errors.New("fallback email service is not configured")
	}
	subject := fmt.Sprintf("[%s] 账号额度已刷新 / Quota Refreshed - %s", sanitizeEmailHeader(siteName), sanitizeEmailHeader(account.Name))
	body := buildQuotaRefreshEmailBody(account.ID, html.EscapeString(account.Name), html.EscapeString(account.Platform), summaryHTML, html.EscapeString(siteName))
	return s.emailService.SendEmail(ctx, to, subject, body)
}

func (s *QuotaRefreshNotifyService) listAdminEmails(ctx context.Context) []string {
	if s.userRepo == nil {
		return nil
	}
	noSubs := false
	seen := make(map[string]bool)
	var out []string
	for page := 1; ; page++ {
		users, pag, err := s.userRepo.ListWithFilters(ctx, pagination.PaginationParams{Page: page, PageSize: 100}, UserListFilters{
			Role:                 RoleAdmin,
			Status:               StatusActive,
			IncludeSubscriptions: &noSubs,
		})
		if err != nil {
			slog.Warn("quota_refresh_notify: list admins failed", "error", err)
			return out
		}
		for i := range users {
			email := strings.TrimSpace(users[i].Email)
			if email == "" {
				continue
			}
			lower := strings.ToLower(email)
			if seen[lower] {
				continue
			}
			seen[lower] = true
			out = append(out, email)
		}
		if pag == nil || page >= pag.Pages || len(users) == 0 {
			break
		}
	}
	return out
}

func (s *QuotaRefreshNotifyService) getSiteName(ctx context.Context) string {
	if s.settingRepo == nil {
		return defaultSiteName
	}
	name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || name == "" {
		return defaultSiteName
	}
	return name
}

// ---------- 纯函数：窗口抽取 / 检测 / 快照 ----------

// extractUsageWindows 从 UsageInfo 抽取可比较的窗口快照。
func extractUsageWindows(usage *UsageInfo) map[string]usageWindowSnap {
	if usage == nil {
		return nil
	}
	out := make(map[string]usageWindowSnap)
	addProgress := func(key string, p *UsageProgress) {
		if p == nil {
			return
		}
		out[key] = usageWindowSnap{Utilization: p.Utilization, ResetsAt: p.ResetsAt}
	}
	addProgress("five_hour", usage.FiveHour)
	addProgress("seven_day", usage.SevenDay)
	addProgress("seven_day_sonnet", usage.SevenDaySonnet)
	addProgress("seven_day_fable", usage.SevenDayFable)
	addProgress("gemini_shared_daily", usage.GeminiSharedDaily)
	addProgress("gemini_pro_daily", usage.GeminiProDaily)
	addProgress("gemini_flash_daily", usage.GeminiFlashDaily)

	for model, q := range usage.AntigravityQuota {
		if q == nil {
			continue
		}
		var resetsAt *time.Time
		if q.ResetTime != "" {
			if t, err := time.Parse(time.RFC3339, q.ResetTime); err == nil {
				resetsAt = &t
			} else if t, err := time.Parse(time.RFC3339Nano, q.ResetTime); err == nil {
				resetsAt = &t
			}
		}
		out["antigravity:"+model] = usageWindowSnap{
			Utilization: float64(q.Utilization),
			ResetsAt:    resetsAt,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// detectQuotaRefresh 对比旧新窗口，返回已刷新的窗口列表。
func detectQuotaRefresh(oldWindows, newWindows map[string]usageWindowSnap, now time.Time) []refreshedWindow {
	if len(oldWindows) == 0 || len(newWindows) == 0 {
		return nil
	}
	var out []refreshedWindow
	for key, oldW := range oldWindows {
		newW, ok := newWindows[key]
		if !ok {
			continue
		}
		if isWindowRefreshed(oldW, newW, now) {
			out = append(out, refreshedWindow{
				Key:         key,
				OldUtil:     oldW.Utilization,
				NewUtil:     newW.Utilization,
				OldResetsAt: oldW.ResetsAt,
				NewResetsAt: newW.ResetsAt,
			})
		}
	}
	return out
}

// filterRefreshedBySelectedWindows 按配置的窗口多选过滤。
// selected 为空时保留全部（兼容旧配置 / 全选语义）。
// selected 含 "antigravity" 时匹配所有 antigravity:<model> 窗口。
func filterRefreshedBySelectedWindows(refreshed []refreshedWindow, selected []string) []refreshedWindow {
	if len(refreshed) == 0 || len(selected) == 0 {
		return refreshed
	}
	allow := make(map[string]bool, len(selected))
	antigravityAll := false
	for _, k := range selected {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if k == QuotaRefreshWindowAntigravityAll {
			antigravityAll = true
			continue
		}
		allow[k] = true
	}
	// selected 解析后仍为空 → 不过滤
	if len(allow) == 0 && !antigravityAll {
		return refreshed
	}
	out := make([]refreshedWindow, 0, len(refreshed))
	for _, w := range refreshed {
		if allow[w.Key] {
			out = append(out, w)
			continue
		}
		if antigravityAll && strings.HasPrefix(w.Key, "antigravity:") {
			out = append(out, w)
		}
	}
	return out
}

// isWindowRefreshed 判定单个窗口是否发生「额度刷新」。
func isWindowRefreshed(oldW, newW usageWindowSnap, now time.Time) bool {
	// 规则 1：reset 时间滚到明显更晚的新窗口，且用量明显下降
	if oldW.ResetsAt != nil && newW.ResetsAt != nil {
		if newW.ResetsAt.After(oldW.ResetsAt.Add(quotaRefreshResetsAtMinAdvance)) &&
			newW.Utilization+quotaRefreshUtilDropMin < oldW.Utilization {
			return true
		}
	}
	// 规则 2：旧重置点已过，且用量显著下降（或从高位回到低位）
	if oldW.ResetsAt != nil && !oldW.ResetsAt.After(now) {
		drop := oldW.Utilization - newW.Utilization
		if drop >= quotaRefreshUtilDropSignificant {
			return true
		}
		if oldW.Utilization >= quotaRefreshUtilHigh && newW.Utilization <= quotaRefreshUtilLow {
			return true
		}
	}
	return false
}

func parseQuotaRefreshSnapshot(extra map[string]any) *quotaRefreshSnapshot {
	if extra == nil {
		return nil
	}
	raw, ok := extra[extraKeyQuotaRefreshNotifySnapshot]
	if !ok || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	snap := &quotaRefreshSnapshot{Windows: make(map[string]usageWindowSnap)}
	if s, ok := m["sampled_at"].(string); ok {
		snap.SampledAt = s
	}
	windowsRaw, ok := m["windows"].(map[string]any)
	if !ok {
		return snap
	}
	for k, v := range windowsRaw {
		wm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		w := usageWindowSnap{}
		switch u := wm["utilization"].(type) {
		case float64:
			w.Utilization = u
		case int:
			w.Utilization = float64(u)
		case int64:
			w.Utilization = float64(u)
		}
		if rs, ok := wm["resets_at"].(string); ok && rs != "" {
			if t, err := time.Parse(time.RFC3339, rs); err == nil {
				w.ResetsAt = &t
			} else if t, err := time.Parse(time.RFC3339Nano, rs); err == nil {
				w.ResetsAt = &t
			}
		}
		snap.Windows[k] = w
	}
	return snap
}

func snapshotToMap(snap quotaRefreshSnapshot) map[string]any {
	windows := make(map[string]any, len(snap.Windows))
	for k, w := range snap.Windows {
		entry := map[string]any{"utilization": w.Utilization}
		if w.ResetsAt != nil {
			entry["resets_at"] = w.ResetsAt.UTC().Format(time.RFC3339)
		}
		windows[k] = entry
	}
	return map[string]any{
		"sampled_at": snap.SampledAt,
		"windows":    windows,
	}
}

func parseWindowNotifiedAt(extra map[string]any) map[string]time.Time {
	encoded := make(map[string]string)
	out := make(map[string]time.Time)
	if extra == nil {
		return out
	}
	raw := extra[extraKeyQuotaRefreshLastNotifiedWindows]
	data, err := json.Marshal(raw)
	if err != nil || string(data) == "null" {
		return out
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		return out
	}
	for key, value := range encoded {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			out[key] = parsed
		}
	}
	return out
}

func filterDebouncedWindows(windows []refreshedWindow, notifiedAt map[string]time.Time, now time.Time) []refreshedWindow {
	filtered := make([]refreshedWindow, 0, len(windows))
	for _, window := range windows {
		if last, ok := notifiedAt[window.Key]; ok {
			elapsed := now.Sub(last)
			if elapsed >= 0 && elapsed < quotaRefreshNotifyDebounce {
				continue
			}
		}
		filtered = append(filtered, window)
	}
	return filtered
}

func newQuotaRefreshPending(accountID int64, windows []refreshedWindow, snapshot quotaRefreshSnapshot, now time.Time) *quotaRefreshPendingNotification {
	parts := make([]string, 0, len(windows))
	for _, window := range windows {
		reset := "none"
		if window.NewResetsAt != nil {
			reset = window.NewResetsAt.UTC().Format(time.RFC3339Nano)
		}
		parts = append(parts, fmt.Sprintf("%s@%s@%.4f>%.4f", window.Key, reset, window.OldUtil, window.NewUtil))
	}
	sort.Strings(parts)
	return &quotaRefreshPendingNotification{
		EventKey:  fmt.Sprintf("%d:%s", accountID, strings.Join(parts, ",")),
		Snapshot:  snapshot,
		Windows:   append([]refreshedWindow(nil), windows...),
		CreatedAt: now.UTC().Format(time.RFC3339Nano),
	}
}

func formatWindowNotifiedAt(input map[string]time.Time) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func parseQuotaRefreshPending(extra map[string]any) *quotaRefreshPendingNotification {
	if extra == nil || extra[extraKeyQuotaRefreshPending] == nil {
		return nil
	}
	data, err := json.Marshal(extra[extraKeyQuotaRefreshPending])
	if err != nil {
		slog.Warn("quota_refresh_notify: encode pending event failed", "error", err)
		return nil
	}
	var pending quotaRefreshPendingNotification
	if err := json.Unmarshal(data, &pending); err != nil {
		slog.Warn("quota_refresh_notify: decode pending event failed", "error", err)
		return nil
	}
	if pending.EventKey == "" || len(pending.Windows) == 0 {
		slog.Warn("quota_refresh_notify: pending event is incomplete")
		return nil
	}
	if pending.LastNotifiedByKey == nil {
		pending.LastNotifiedByKey = make(map[string]string)
	}
	return &pending
}

func cloneStringMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func windowLabel(key string) string {
	labels := map[string]string{
		"five_hour":           "5h",
		"seven_day":           "7d",
		"seven_day_sonnet":    "7d Sonnet",
		"seven_day_fable":     "7d Fable",
		"gemini_shared_daily": "Gemini Shared Daily",
		"gemini_pro_daily":    "Gemini Pro Daily",
		"gemini_flash_daily":  "Gemini Flash Daily",
	}
	if l, ok := labels[key]; ok {
		return l
	}
	if strings.HasPrefix(key, "antigravity:") {
		return "Antigravity " + strings.TrimPrefix(key, "antigravity:")
	}
	return key
}

func formatWindowsSummary(windows []refreshedWindow) string {
	parts := make([]string, 0, len(windows))
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf("%s: %.0f%% → %.0f%%", windowLabel(w.Key), w.OldUtil, w.NewUtil))
	}
	return strings.Join(parts, "; ")
}

func formatWindowsSummaryHTML(windows []refreshedWindow) string {
	rows := make([]string, 0, len(windows))
	for _, w := range windows {
		rows = append(rows, fmt.Sprintf(
			`<tr><td style="padding:6px 0;color:#666;">%s</td><td style="padding:6px 0;font-weight:bold;text-align:right;">%.0f%% → %.0f%%</td></tr>`,
			html.EscapeString(windowLabel(w.Key)),
			w.OldUtil,
			w.NewUtil,
		))
	}
	return `<table style="width:100%;border-collapse:collapse;">` + strings.Join(rows, "") + `</table>`
}

const quotaRefreshEmailTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #f5f5f5; margin: 0; padding: 20px; }
        .container { max-width: 600px; margin: 0 auto; background-color: #fff; border-radius: 8px; overflow: hidden; box-shadow: 0 2px 8px rgba(0,0,0,0.1); }
        .header { background: linear-gradient(135deg, #10b981 0%%, #059669 100%%); color: white; padding: 30px; text-align: center; }
        .header h1 { margin: 0; font-size: 24px; }
        .content { padding: 40px 30px; }
        .metric { display: flex; justify-content: space-between; padding: 12px 0; border-bottom: 1px solid #eee; }
        .metric-label { color: #666; }
        .metric-value { font-weight: bold; color: #333; }
        .info { color: #666; font-size: 14px; line-height: 1.6; margin-top: 20px; text-align: center; }
        .footer { background-color: #f8f9fa; padding: 20px; text-align: center; color: #999; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header"><h1>%s</h1></div>
        <div class="content">
            <p style="font-size: 18px; color: #333; text-align: center;">账号额度已刷新 / Account Quota Refreshed</p>
            <div class="metric"><span class="metric-label">账号 ID / Account ID</span><span class="metric-value">#%d</span></div>
            <div class="metric"><span class="metric-label">账号 / Account</span><span class="metric-value">%s</span></div>
            <div class="metric"><span class="metric-label">平台 / Platform</span><span class="metric-value">%s</span></div>
            <div style="margin-top: 16px;">
                <p style="color:#666;font-size:14px;margin-bottom:8px;">刷新窗口 / Refreshed Windows</p>
                %s
            </div>
            <div class="info">
                <p>关注的上游账号额度窗口已重置，可继续调度使用。</p>
                <p>A watched upstream account usage window has reset and capacity is available again.</p>
            </div>
        </div>
        <div class="footer"><p>此邮件由系统自动发送，请勿回复。</p></div>
    </div>
</body>
</html>`

func buildQuotaRefreshEmailBody(accountID int64, accountName, platform, windowsHTML, siteName string) string {
	return fmt.Sprintf(quotaRefreshEmailTemplate, siteName, accountID, accountName, platform, windowsHTML)
}
