import { useEffect, useMemo, useState } from 'react'
import {
  aiCostApi,
  formatCostDual,
  formatModelPrice,
  type AIModelInfo,
  type CostSummary,
} from '../api/aiCost'
import { Cpu, Sparkles, AlertCircle, Loader2 } from 'lucide-react'

// AIModelSelector · AI 模型选择 + 成本预估面板
//
// 用途：嵌入"AI 配置"页或"懒人入库"前置询问框，让用户：
//   1. 在 provider 下挑选具体 model（带价格 + 上下文长度 + 推荐徽章）
//   2. 实时查看该 model 的"样例单次估价"（默认 1K input + 0.5K output）
//   3. 查看当前 AIService 累计花费
//
// 受控用法（推荐）：
//   <AIModelSelector
//     provider="openai"
//     value={model}
//     onChange={(provider, model) => ...}
//     showSummary
//   />

interface Props {
  /** 当前 provider；变化会触发 model 列表刷新 */
  provider: string
  /** 当前选中的 model id */
  value?: string
  /** 选中变化回调 */
  onChange?: (provider: string, modelId: string, info: AIModelInfo) => void
  /** 是否显示底部"累计花费"摘要（默认 true） */
  showSummary?: boolean
  /** 紧凑模式（用于嵌入小弹窗） */
  compact?: boolean
}

export default function AIModelSelector({
  provider,
  value,
  onChange,
  showSummary = true,
  compact = false,
}: Props) {
  const [models, setModels] = useState<AIModelInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [errorMsg, setErrorMsg] = useState('')
  const [summary, setSummary] = useState<CostSummary | null>(null)

  // 拉取该 provider 下的模型
  useEffect(() => {
    if (!provider) {
      setModels([])
      return
    }
    let cancelled = false
    setLoading(true)
    setErrorMsg('')
    aiCostApi
      .listModels(provider)
      .then((resp) => {
        if (cancelled) return
        const list = resp.data?.data || []
        setModels(list)
        // 若当前 value 不在列表中，默认选第一条 recommended（或第一条）
        if ((!value || !list.find((m) => m.id === value)) && list.length > 0 && onChange) {
          const pick = list.find((m) => m.recommended) || list[0]
          onChange(pick.provider, pick.id, pick)
        }
      })
      .catch((e: any) => {
        if (cancelled) return
        setErrorMsg(e?.response?.data?.error || e?.message || '加载模型列表失败')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider])

  // 拉取累计花费
  useEffect(() => {
    if (!showSummary) return
    let cancelled = false
    aiCostApi
      .summary()
      .then((resp) => {
        if (!cancelled) setSummary(resp.data?.data || null)
      })
      .catch(() => {
        if (!cancelled) setSummary(null)
      })
    return () => {
      cancelled = true
    }
  }, [showSummary, value])

  const selected = useMemo(() => models.find((m) => m.id === value) || null, [models, value])

  // 样例估价（1K in + 0.5K out）— 直接前端算（catalog 已在 models 里）
  const sampleEst = useMemo(() => {
    if (!selected) return null
    const usd = (1000 / 1000) * selected.input_price + (500 / 1000) * selected.output_price
    return usd
  }, [selected])

  return (
    <div className={compact ? 'space-y-2' : 'space-y-3'}>
      {/* 标题 */}
      {!compact && (
        <div className="flex items-center gap-2">
          <Cpu size={14} className="text-neon" />
          <span className="text-sm font-medium">AI 模型</span>
          {provider && (
            <span className="text-xs" style={{ color: 'var(--text-tertiary)' }}>
              · {provider}
            </span>
          )}
        </div>
      )}

      {/* 错误 */}
      {errorMsg && (
        <div
          className="flex items-start gap-2 rounded-lg px-3 py-2 text-xs"
          style={{ background: 'rgba(244,63,94,0.1)', color: '#fca5a5' }}
        >
          <AlertCircle size={14} className="mt-0.5 flex-shrink-0" />
          <span>{errorMsg}</span>
        </div>
      )}

      {/* loading */}
      {loading && (
        <div className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-tertiary)' }}>
          <Loader2 size={14} className="animate-spin" />
          加载模型列表…
        </div>
      )}

      {/* 模型列表 */}
      {!loading && models.length > 0 && (
        <div className="space-y-1.5">
          {models.map((m) => {
            const active = m.id === value
            const sampleCost = (1000 / 1000) * m.input_price + (500 / 1000) * m.output_price
            return (
              <button
                key={m.id}
                onClick={() => onChange?.(m.provider, m.id, m)}
                className="w-full rounded-lg px-3 py-2 text-left transition-all"
                style={{
                  background: active ? 'var(--neon-tint)' : 'var(--bg-base)',
                  border: `1px solid ${active ? 'var(--neon)' : 'var(--border-default)'}`,
                }}
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span
                      className="text-sm font-medium"
                      style={{ color: active ? 'var(--neon)' : 'var(--text-primary)' }}
                    >
                      {m.label}
                    </span>
                    {m.recommended && (
                      <span
                        className="flex items-center gap-0.5 rounded-full px-1.5 py-0.5 text-[10px]"
                        style={{ background: 'rgba(34,211,238,0.15)', color: '#67e8f9' }}
                      >
                        <Sparkles size={10} />
                        推荐
                      </span>
                    )}
                  </div>
                  <span className="text-xs tabular-nums" style={{ color: 'var(--text-secondary)' }}>
                    {sampleCost === 0 ? '免费' : `≈ ${formatCostDual(sampleCost)}`}
                  </span>
                </div>
                <div className="mt-1 flex items-center gap-2 text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
                  <span className="truncate font-mono">{m.id}</span>
                  <span>·</span>
                  <span>{(m.context_length / 1000).toFixed(0)}K ctx</span>
                  <span>·</span>
                  <span>{formatModelPrice(m)}</span>
                </div>
                {!compact && m.notes && (
                  <div className="mt-1 text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
                    {m.notes}
                  </div>
                )}
              </button>
            )
          })}
        </div>
      )}

      {!loading && !errorMsg && models.length === 0 && (
        <div className="text-xs" style={{ color: 'var(--text-tertiary)' }}>
          provider={provider || '(空)'} 下暂无内置模型
        </div>
      )}

      {/* 样例估价提示 */}
      {selected && sampleEst !== null && !compact && (
        <div className="text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
          💡 样例估算（1000 输入 + 500 输出 tokens）：
          <span className="ml-1 font-medium" style={{ color: 'var(--text-secondary)' }}>
            {sampleEst === 0 ? '免费' : formatCostDual(sampleEst)}
          </span>
        </div>
      )}

      {/* 累计花费 */}
      {showSummary && summary && (
        <div
          className="rounded-lg px-3 py-2 text-xs"
          style={{ background: 'var(--bg-base)', border: '1px solid var(--border-default)' }}
        >
          <div className="flex items-center justify-between">
            <span style={{ color: 'var(--text-secondary)' }}>累计花费（估算）</span>
            <span className="font-semibold tabular-nums" style={{ color: 'var(--neon)' }}>
              {summary.estimate?.cost_usd
                ? formatCostDual(summary.estimate.cost_usd, summary.estimate.usd_to_cny_rate)
                : '$0.0000 (¥0.000)'}
            </span>
          </div>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
            <span>当月调用：{summary.monthly_calls}</span>
            <span>输入 tokens：{summary.total_prompt_tokens.toLocaleString()}</span>
            <span>输出 tokens：{summary.total_completion_tokens.toLocaleString()}</span>
          </div>
          {summary.estimate?.fallback && (
            <div className="mt-1 text-[10px]" style={{ color: '#fbbf24' }}>
              ⚠ {summary.estimate.note}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
