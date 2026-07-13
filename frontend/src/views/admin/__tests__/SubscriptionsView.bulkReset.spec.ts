import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SubscriptionsView from '../SubscriptionsView.vue'

const api = vi.hoisted(() => ({
  list: vi.fn(),
  resetQuota: vi.fn(),
  previewBulkResetQuota: vi.fn(),
  bulkResetQuota: vi.fn(),
  getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    subscriptions: {
      list: api.list,
      resetQuota: api.resetQuota,
      previewBulkResetQuota: api.previewBulkResetQuota,
      bulkResetQuota: api.bulkResetQuota
    },
    groups: { getAll: api.getAllGroups },
    usage: { searchUsers: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const subscriptions = [
  {
    id: 11,
    user_id: 101,
    group_id: 1,
    status: 'active',
    starts_at: '2026-01-01T00:00:00Z',
    daily_usage_usd: 1,
    weekly_usage_usd: 2,
    monthly_usage_usd: 3,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    expires_at: '2027-01-01T00:00:00Z',
    user: { email: 'one@example.com' },
    group: { id: 1, name: 'Group 1', status: 'active', subscription_type: 'subscription', platform: 'openai', rate_multiplier: 1 }
  },
  {
    id: 22,
    user_id: 202,
    group_id: 2,
    status: 'active',
    starts_at: '2026-01-01T00:00:00Z',
    daily_usage_usd: 1,
    weekly_usage_usd: 2,
    monthly_usage_usd: 3,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    expires_at: '2027-01-01T00:00:00Z',
    user: { email: 'two@example.com' },
    group: { id: 2, name: 'Group 2', status: 'active', subscription_type: 'subscription', platform: 'openai', rate_multiplier: 1 }
  }
]

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <slot name="header-select" />
      <div v-for="row in data" :key="row.id">
        <slot name="cell-select" :row="row" />
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}

const BaseDialogStub = {
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
}

function mountView() {
  return mount(SubscriptionsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        Pagination: true,
        EmptyState: true,
        Select: true,
        GroupBadge: true,
        GroupOptionItem: true,
        Icon: true,
        RouterLink: true,
        Teleport: true
      }
    }
  })
}

describe('admin SubscriptionsView bulk quota reset', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
    api.list.mockResolvedValue({ items: subscriptions, total: 2, page: 1, page_size: 20, pages: 1 })
    api.getAllGroups.mockResolvedValue(subscriptions.map((item) => item.group))
    api.previewBulkResetQuota.mockResolvedValue({ total: 1, valid: 1, failed: 0, failures: [] })
    api.bulkResetQuota.mockResolvedValue({ total: 1, success: 1, failed: 0, success_ids: [11], failures: [] })
    api.resetQuota.mockResolvedValue(subscriptions[0])
  })

  it('clears a group exclusion when the group is removed and selected again', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="group-select-open"]').trigger('click')
    await wrapper.get('[data-test="group-select-1"]').setValue(true)
    await wrapper.get('[data-test="subscription-select-11"]').setValue(false)
    await wrapper.get('[data-test="group-select-1"]').setValue(false)
    await wrapper.get('[data-test="group-select-1"]').setValue(true)
    await wrapper.get('[data-test="bulk-reset-open"]').trigger('click')
    await flushPromises()

    expect(api.previewBulkResetQuota).toHaveBeenLastCalledWith(expect.objectContaining({
      target: { group_ids: [1], subscription_ids: [], excluded_subscription_ids: [] }
    }))
  })

  it('previews the effective count whenever the selection rule changes', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="group-select-open"]').trigger('click')
    await wrapper.get('[data-test="group-select-1"]').setValue(true)
    await flushPromises()

    expect(api.previewBulkResetQuota).toHaveBeenLastCalledWith(expect.objectContaining({
      target: { group_ids: [1], subscription_ids: [], excluded_subscription_ids: [] }
    }))
    expect(wrapper.text()).toContain('admin.subscriptions.bulk.actualSelectionCount')
  })

  it('keeps every failed subscription as a manual selection after partial failure', async () => {
    api.previewBulkResetQuota.mockResolvedValue({ total: 2, valid: 2, failed: 0, failures: [] })
    api.bulkResetQuota.mockResolvedValue({
      total: 2,
      success: 0,
      failed: 2,
      success_ids: [],
      failures: [
        { subscription_id: 11, error: 'stale group member' },
        { subscription_id: 22, error: 'locked' }
      ]
    })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="group-select-open"]').trigger('click')
    await wrapper.get('[data-test="group-select-1"]').setValue(true)
    await wrapper.get('[data-test="subscription-select-22"]').setValue(true)
    await wrapper.get('[data-test="bulk-reset-open"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="reset-confirm"]').trigger('click')
    await flushPromises()

    expect((wrapper.get('[data-test="subscription-select-11"]').element as HTMLInputElement).checked).toBe(true)
    expect((wrapper.get('[data-test="subscription-select-22"]').element as HTMLInputElement).checked).toBe(true)
  })

  it('sends the chosen windows for a single subscription reset', async () => {
    const wrapper = mountView()
    await flushPromises()
    const resetAction = wrapper.findAll('button').find((button) => button.text().includes('admin.subscriptions.resetQuota'))
    expect(resetAction).toBeTruthy()
    await resetAction!.trigger('click')
    await wrapper.get('[data-test="reset-window-daily"]').setValue(false)
    await wrapper.get('[data-test="reset-window-monthly"]').setValue(false)
    await wrapper.get('[data-test="reset-confirm"]').trigger('click')
    await flushPromises()

    expect(api.resetQuota).toHaveBeenCalledWith(11, { daily: false, weekly: true, monthly: false })
  })
})
