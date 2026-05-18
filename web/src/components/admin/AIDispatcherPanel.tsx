/**
 * AIDispatcherPanel - V7：AI 智能调度 / 用量监控 / 故障转移控制台
 *
 * 设计：作为 AITab 的子区块挂载（不侵入大文件）。
 *
 * 包含以下内容：
 *   1. 当前调度状态 - preferred / current_active / 月用量进度条
 *   2. 切换链路 - 显示 chain，并提供"调整链/启停"
 *   3. 手动控制 - 强制切换 / 恢复主 provider
 *   4. 切换审计日志 - 最近 100 条
 *
 * 注：“一键配置通义千问”已下沉到 AITab 的通义千问子选项卡中，不再在调度面板重复展示。
 */
import { useEffect, useMemo, useState } from 'react'
import { aiApi } from '@/api/ai'
import { useToast } from '@/components/Toast'
import { useDialog } from '@/components/Dialog'
import type {
  AIProviderPreset,
  AIRouterSnapshot,
  AIFailoverLog,
  AIUsageReport,
} from '@/types'

interface AIDispatcherPanelProps {
  onConfigChanged?: () => void
}

export default function AIDispatcherPanel({ onConfigChanged }: AIDispatcherPanelProps) {
  const toast = useToast()
  const dialog = useDialog()

  const [presets, setPresets] = useState<AIProviderPreset[]>([])
  const [snapshot, setSnapshot] = useState<AIRouterSnapshot | null>(null)
  const [logs, setLogs] = useState<AIFailoverLog[]>([])
  const [usage, setUsage] = useState<AIUsageReport | null>(null)
  const [usageRange, setUsageRange] = useState<'day' | 'week' | 'month' | 'year'>('month')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)

  const loadAll = async () => {
    try {
      const [p, s, l, u] = await Promise.all([
        aiApi.listProviderPresets(),
        aiApi.getRouterSnapshot(),
        aiApi.listFailoverLogs(50),
        aiApi.getUsageBuckets(usageRange),
      ])
      setPresets(p.data.data ?? [])
      // 后端 nil slice 会被 Go 序列化为 null，这里统一兜底
      const snap = s.data.data
      if (snap) {
        snap.chain = snap.chain ?? []
        snap.provider_totals = snap.provider_totals ?? []
      }
      setSnapshot(snap)
      setLogs(l.data.data ?? [])
      const usageData = u.data.data
      if (usageData) {
        usageData.buckets = usageData.buckets ?? []
        usageData.provider_totals = usageData.provider_totals ?? []
      }
      setUsage(usageData)
    } catch (err: any) {
      toast.error('加载 AI 调度状态失败：' + (err?.response?.data?.error || err?.message || ''))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadAll()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!loading) {
      aiApi
        .getUsageBuckets(usageRange)
        .then((r) => {
          const data = r.data.data
          if (data) {
            data.buckets = data.buckets ?? []
            data.provider_totals = data.provider_totals ?? []
          }
          setUsage(data)
        })
        .catch(() => {})
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [usageRange])

  const handleSwitch = async (provider: string) => {
    const ok = await dialog.confirm({
      title: '切换 AI Provider',
      message: `确定要将所有 AI 调用切换到 ${provider} 吗？\n后续 LLM 请求会立即走 ${provider}，可在"恢复主 provider"按钮一键还原。`,
      confirmText: '切换',
      variant: 'primary',
    })
    if (!ok) return
    setBusy(true)
    try {
      await aiApi.forceSwitchProvider(provider)
      toast.success('已切换到 ' + provider)
      await loadAll()
      onConfigChanged?.()
    } catch (err: any) {
      toast.error('切换失败：' + (err?.response?.data?.error || err?.message || ''))
    } finally {
      setBusy(false)
    }
  }

  const handleRestore = async () => {
    const ok = await dialog.confirm({
      title: '恢复主 Provider',
      message: '确定要恢复到主 provider 吗？',
      confirmText: '恢复',
      variant: 'primary',
    })
    if (!ok) return
    setBusy(true)
    try {
      await aiApi.restoreProvider()
      toast.success('已恢复主 provider')
      await loadAll()
      onConfigChanged?.()
    } catch (err: any) {
      toast.error('恢复失败：' + (err?.response?.data?.error || err?.message || ''))
    } finally {
      setBusy(false)
    }
  }

  const usagePct = useMemo(() => {
    if (!snapshot || snapshot.monthly_token_budget <= 0) return null
    return Math.min(100, snapshot.monthly_token_pct)
  }, [snapshot])

  const usageColor = useMemo(() => {
    if (usagePct == null) return 'bg-blue-500'
    if (usagePct >= 100) return 'bg-red-500'
    if (usagePct >= (snapshot?.warning_threshold_pct ?? 80)) return 'bg-yellow-500'
    return 'bg-emerald-500'
  }, [usagePct, snapshot])

  if (loading) {
    return (
      <div className="glass-panel rounded-xl p-6 text-sm text-theme-muted">
        正在加载 AI 调度状态...
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* —————————— 1. 当前调度状态 —————————— */}
      <div className="glass-panel rounded-xl p-5">
        <div className="mb-4 flex items-center justify-between">
          <div className="font-display text-lg font-semibold tracking-wide text-theme-primary flex items-center gap-2">
            <span className="text-neon">🎯</span> 当前调度状态
          </div>
          <button
            onClick={loadAll}
            disabled={busy}
            className="rounded-lg px-3 py-1 text-xs text-theme-secondary hover:text-theme-primary transition-colors disabled:opacity-50"
            style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }}
          >
            刷新
          </button>
        </div>
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          <div
            className="rounded-lg p-3"
            style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }}
          >
            <div className="text-xs text-theme-muted">主 Provider（用户偏好）</div>
            <div className="mt-1 text-lg font-semibold text-theme-primary">
              {snapshot?.preferred_provider || '—'}
            </div>
          </div>
          <div
            className="rounded-lg p-3"
            style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }}
          >
            <div className="text-xs text-theme-muted">此刻生效（实际调用）</div>
            <div className="mt-1 text-lg font-semibold text-emerald-400">
              {snapshot?.current_active || snapshot?.preferred_provider || '—'}
              {snapshot?.current_active && snapshot.current_active !== snapshot.preferred_provider && (
                <span className="ml-2 rounded bg-yellow-500/20 px-2 py-0.5 text-xs text-yellow-300">
                  已切换
                </span>
              )}
            </div>
            {snapshot?.current_model && (
              <div className="mt-1 truncate text-[11px] text-theme-muted">
                model: <span className="font-mono text-theme-secondary">{snapshot.current_model}</span>
                {snapshot.preferred_model &&
                  snapshot.current_model !== snapshot.preferred_model && (
                    <span className="ml-1.5 rounded bg-yellow-500/15 px-1 py-0.5 text-[10px] text-yellow-300">
                      已切链
                    </span>
                  )}
              </div>
            )}
          </div>
        </div>

        {/* 月度 token 进度 */}
        {snapshot && snapshot.monthly_token_budget > 0 && (
          <div className="mt-4">
            <div className="mb-1 flex justify-between text-xs">
              <span className="text-theme-muted">本月 Token 用量</span>
              <span className="text-theme-secondary">
                {snapshot.monthly_token_used.toLocaleString()} / {snapshot.monthly_token_budget.toLocaleString()}
                <span className="ml-2 text-theme-muted">({snapshot.monthly_token_pct}%)</span>
              </span>
            </div>
            <div
              className="h-2 overflow-hidden rounded-full"
              style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }}
            >
              <div
                className={`h-full transition-all ${usageColor}`}
                style={{ width: `${usagePct ?? 0}%` }}
              />
            </div>
            <div className="mt-1 text-xs text-theme-muted">
              预警阈值 {snapshot.warning_threshold_pct}%，
              {snapshot.failover_enabled
                ? `配额耗尽后将自动切换到链路下一个 provider`
                : '⚠ 故障转移已关闭，配额耗尽时将直接拒绝请求'}
            </div>
          </div>
        )}

        {/* 各 provider 当月对比 */}
        {snapshot?.provider_totals && Array.isArray(snapshot.provider_totals) && snapshot.provider_totals.length > 0 && (
          <div
            className="mt-4 overflow-hidden rounded-lg"
            style={{ border: '1px solid var(--border-subtle)' }}
          >
            <table className="w-full text-xs">
              <thead className="text-theme-muted" style={{ background: 'var(--bg-surface)' }}>
                <tr>
                  <th className="px-3 py-2 text-left font-medium">Provider</th>
                  <th className="px-3 py-2 text-right font-medium">调用次数</th>
                  <th className="px-3 py-2 text-right font-medium">Token</th>
                  <th className="px-3 py-2 text-right font-medium">成本(¥)</th>
                  <th className="px-3 py-2 text-center font-medium">状态</th>
                </tr>
              </thead>
              <tbody>
                {snapshot.provider_totals.map((p) => (
                  <tr key={p.provider} style={{ borderTop: '1px solid var(--border-subtle)' }}>
                    <td className="px-3 py-2 font-medium text-theme-primary">{p.provider}</td>
                    <td className="px-3 py-2 text-right text-theme-secondary">{p.calls}</td>
                    <td className="px-3 py-2 text-right text-theme-secondary">{p.total_tokens.toLocaleString()}</td>
                    <td className="px-3 py-2 text-right text-theme-secondary">¥{p.cost_cny.toFixed(2)}</td>
                    <td className="px-3 py-2 text-center">
                      {p.configured ? (
                        <span className="rounded bg-emerald-500/20 px-2 py-0.5 text-emerald-300">已配置</span>
                      ) : (
                        <span className="rounded bg-gray-500/20 px-2 py-0.5 text-theme-muted">未配置</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* —————————— 2. 切换链路 + 手动控制 —————————— */}
      <div className="glass-panel rounded-xl p-5">
        <div className="mb-3 font-display text-lg font-semibold tracking-wide text-theme-primary flex items-center gap-2">
          <span className="text-neon">🔁</span> 切换链 &amp; 手动控制
        </div>
        <div className="mb-4 flex flex-wrap items-center gap-2 text-sm">
          {(snapshot?.chain ?? []).length === 0 && (
            <span className="text-theme-muted">链路为空（请配置 ai.failover.chain，并填好备用 provider 的 api_key）</span>
          )}
          {(snapshot?.chain ?? []).map((p, i) => {
            const isActive = p === snapshot?.current_active
            return (
              <span
                key={p}
                className={`flex items-center gap-1 rounded-lg px-3 py-1 ${
                  isActive ? 'text-emerald-300' : 'text-theme-secondary'
                }`}
                style={
                  isActive
                    ? { background: 'rgba(16,185,129,0.10)', border: '1px solid rgba(16,185,129,0.40)' }
                    : { background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }
                }
              >
                <span className="text-xs text-theme-muted">{i === 0 ? '主' : `备${i}`}</span>
                <span className="font-medium">{p}</span>
                {isActive && <span className="ml-1 text-xs">●</span>}
                {i < (snapshot?.chain ?? []).length - 1 && <span className="ml-1 text-theme-muted">▶</span>}
              </span>
            )
          })}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <select
            id="ai-router-target"
            className="rounded-lg px-3 py-2 text-sm text-theme-primary focus:outline-none"
            style={{
              background: 'var(--bg-surface)',
              border: '1px solid var(--border-subtle)',
            }}
            disabled={busy}
            defaultValue=""
          >
            <option value="" disabled>
              选择切换目标...
            </option>
            {presets.map((p) => (
              <option key={p.provider} value={p.provider}>
                {p.label}（{p.provider}）
              </option>
            ))}
          </select>
          <button
            disabled={busy}
            onClick={() => {
              const sel = document.getElementById('ai-router-target') as HTMLSelectElement
              if (sel?.value) handleSwitch(sel.value)
            }}
            className="rounded-lg px-4 py-2 text-sm font-medium text-white disabled:opacity-50 transition-opacity"
            style={{
              background: 'linear-gradient(135deg, var(--neon-blue), var(--neon-purple))',
              boxShadow: '0 0 12px var(--neon-blue-25)',
            }}
          >
            手动切换
          </button>
          <button
            disabled={busy || snapshot?.current_active === snapshot?.preferred_provider}
            onClick={handleRestore}
            className="rounded-lg px-4 py-2 text-sm font-medium text-emerald-300 disabled:opacity-50 transition-colors"
            style={{
              background: 'rgba(16,185,129,0.10)',
              border: '1px solid rgba(16,185,129,0.40)',
            }}
          >
            ↩ 恢复主 Provider
          </button>
          {snapshot?.auto_recover_after_min ? (
            <span className="text-xs text-theme-muted">
              自动恢复窗口：{snapshot.auto_recover_after_min} 分钟
            </span>
          ) : (
            <span className="text-xs text-theme-muted">未开启自动恢复</span>
          )}
        </div>
      </div>

      {/* —————————— 3. 用量曲线 —————————— */}
      <div className="glass-panel rounded-xl p-5">
        <div className="mb-3 flex items-center justify-between">
          <div className="font-display text-lg font-semibold tracking-wide text-theme-primary flex items-center gap-2">
            <span className="text-neon">📊</span> 用量趋势
          </div>
          <select
            value={usageRange}
            onChange={(e) => setUsageRange(e.target.value as 'day' | 'week' | 'month' | 'year')}
            className="rounded px-2 py-1 text-xs text-theme-secondary focus:outline-none"
            style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }}
          >
            <option value="day">最近 1 天</option>
            <option value="week">最近 7 天</option>
            <option value="month">最近 30 天</option>
            <option value="year">最近 1 年</option>
          </select>
        </div>
        {!usage || !usage.buckets || usage.buckets.length === 0 ? (
          <div className="py-6 text-center text-sm text-theme-muted">该时间段内还没有 AI 调用记录</div>
        ) : (
          <div className="space-y-1">
            {(() => {
              const max = Math.max(...usage.buckets.map((b) => b.total_tokens), 1)
              return usage.buckets.map((b) => (
                <div key={b.bucket} className="flex items-center gap-3 text-xs">
                  <span className="w-24 shrink-0 text-theme-muted">{b.bucket}</span>
                  <div
                    className="relative h-5 flex-1 overflow-hidden rounded"
                    style={{ background: 'var(--bg-surface)', border: '1px solid var(--border-subtle)' }}
                  >
                    <div
                      className="h-full"
                      style={{
                        width: `${(b.total_tokens / max) * 100}%`,
                        background: 'linear-gradient(90deg, var(--neon-blue), var(--neon-purple))',
                        opacity: 0.85,
                      }}
                    />
                    <div className="absolute inset-0 flex items-center justify-end px-2 font-mono text-[11px] text-theme-primary">
                      {b.total_tokens.toLocaleString()} tokens · ¥{b.cost_cny.toFixed(2)}
                    </div>
                  </div>
                </div>
              ))
            })()}
          </div>
        )}
      </div>

      {/* —————————— 4. 切换审计日志 —————————— */}
      <div className="glass-panel rounded-xl p-5">
        <div className="mb-3 font-display text-lg font-semibold tracking-wide text-theme-primary flex items-center gap-2">
          <span className="text-neon">🛟</span> 切换日志
        </div>
        {logs.length === 0 ? (
          <div className="py-4 text-center text-sm text-theme-muted">暂无切换记录</div>
        ) : (
          <div className="max-h-72 overflow-auto">
            <table className="w-full text-xs">
              <thead className="sticky top-0 text-theme-muted" style={{ background: 'var(--bg-surface)' }}>
                <tr>
                  <th className="px-3 py-2 text-left font-medium">时间</th>
                  <th className="px-3 py-2 text-left font-medium">从</th>
                  <th className="px-3 py-2 text-left font-medium">到</th>
                  <th className="px-3 py-2 text-left font-medium">原因</th>
                  <th className="px-3 py-2 text-left font-medium">操作者</th>
                </tr>
              </thead>
              <tbody>
                {logs.map((l) => (
                  <tr key={l.id} style={{ borderTop: '1px solid var(--border-subtle)' }}>
                    <td className="px-3 py-2 text-theme-secondary">{new Date(l.occurred_at).toLocaleString()}</td>
                    <td className="px-3 py-2 text-theme-secondary">{l.from_provider}</td>
                    <td className="px-3 py-2 font-medium text-theme-primary">{l.to_provider}</td>
                    <td className="px-3 py-2 text-theme-secondary">{l.reason}</td>
                    <td className="px-3 py-2 text-theme-muted">{l.operator}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
