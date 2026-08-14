import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import AccountStatsModal from '../AccountStatsModal.vue'
import type { Account, AccountUsageWindowsResponse } from '@/types'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: { en: {} }
})

const { getStats, getUsageWindows } = vi.hoisted(() => ({
  getStats: vi.fn(),
  getUsageWindows: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getStats,
      getUsageWindows
    }
  }
}))

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'Line',
    props: ['data', 'options'],
    template: '<div class="chart-line" />'
  }
}))

function makeAccount(): Account {
  return {
    id: 9,
    name: 'codex-1',
    status: 'active',
    platform: 'openai',
    type: 'oauth'
  } as Account
}

function makeWindowResponse(): AccountUsageWindowsResponse {
  return {
    windows: [
      {
        id: 1,
        account_id: 9,
        window_type: '7d',
        window_start: '2026-08-01T00:00:00Z',
        window_end: '2026-08-08T00:00:00Z',
        status: 'closed',
        peak_used_percent: 40,
        last_used_percent: 38,
        local_cost: 10,
        standard_cost: 12,
        user_cost: 8,
        requests: 4,
        tokens: 100,
        inferred_limit_usd: 30,
        inferred_confidence: 'high',
        model_breakdown: [{ model: 'gpt-5.6-luna', requests: 2, tokens: 50, standard_cost: 7, account_cost: 6 }],
        sampled_at: '2026-08-08T00:00:00Z'
      }
    ],
    daily_by_model: [
      { date: '2026-08-02', model: 'gpt-5.6-luna', requests: 1, tokens: 20, standard_cost: 3, account_cost: 2 }
    ],
    limit_trend: { slope_usd_per_week: 2, trend: 'loosening', sample_count: 2 }
  }
}

describe('AccountStatsModal windows tab', () => {
  it('loads window snapshots and shows inferred limit trend', async () => {
    getStats.mockResolvedValue({
      history: [],
      summary: {
        days: 30,
        actual_days_used: 0,
        total_cost: 0,
        total_user_cost: 0,
        total_standard_cost: 0,
        total_requests: 0,
        total_tokens: 0,
        avg_daily_cost: 0,
        avg_daily_user_cost: 0,
        avg_daily_requests: 0,
        avg_daily_tokens: 0,
        avg_duration_ms: 0,
        today: null,
        highest_cost_day: null,
        highest_request_day: null
      },
      models: [],
      endpoints: [],
      upstream_endpoints: []
    })
    getUsageWindows.mockResolvedValue(makeWindowResponse())

    const wrapper = mount(AccountStatsModal, {
      props: { show: true, account: makeAccount() },
      global: {
        plugins: [i18n],
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          LoadingSpinner: true,
          ModelDistributionChart: true,
          EndpointDistributionChart: true,
          Icon: true
        }
      }
    })
    await flushPromises()

    await wrapper.get('[data-test="stats-tab-windows"]').trigger('click')
    await flushPromises()

    expect(getUsageWindows).toHaveBeenCalledWith(9, { days: 30 })
    expect(wrapper.get('[data-test="limit-trend-badge"]').text()).toContain('limitLoosening')
    expect(wrapper.text()).toContain('gpt-5.6-luna')
    expect(wrapper.text()).toContain('$30.00')
  })

  it('shows empty window copy before any snapshots exist', async () => {
    getStats.mockResolvedValue({
      history: [],
      summary: {
        days: 30,
        actual_days_used: 0,
        total_cost: 0,
        total_user_cost: 0,
        total_standard_cost: 0,
        total_requests: 0,
        total_tokens: 0,
        avg_daily_cost: 0,
        avg_daily_user_cost: 0,
        avg_daily_requests: 0,
        avg_daily_tokens: 0,
        avg_duration_ms: 0,
        today: null,
        highest_cost_day: null,
        highest_request_day: null
      },
      models: [],
      endpoints: [],
      upstream_endpoints: []
    })
    getUsageWindows.mockResolvedValue({
      windows: [],
      daily_by_model: [],
      limit_trend: { slope_usd_per_week: 0, trend: 'insufficient', sample_count: 0 }
    })

    const wrapper = mount(AccountStatsModal, {
      props: { show: true, account: makeAccount() },
      global: {
        plugins: [i18n],
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          LoadingSpinner: true,
          ModelDistributionChart: true,
          EndpointDistributionChart: true,
          Icon: true
        }
      }
    })
    await flushPromises()
    await wrapper.get('[data-test="stats-tab-windows"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="limit-trend-badge"]').text()).toContain('limitInsufficient')
    expect(wrapper.text()).toContain('noWindowSnapshots')
  })
})
