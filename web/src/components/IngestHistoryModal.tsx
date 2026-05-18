import { useEffect, useMemo, useState } from 'react'
import {
  ingestApi,
  parseIngestStats,
  parseLibraryIds,
  statusLabel,
  itemStatusLabel,
  groupItemsByStatus,
  type IngestJob,
  type IngestJobItem,
} from '../api/ingest'
import {
  X,
  History,
  Loader2,
  ChevronRight,
  AlertTriangle,
  CheckCircle2,
  FileSearch,
  RotateCcw,
} from 'lucide-react'

// IngestHistoryModal · 入库历史 + 失败明细
//
// 行为：
//   - 打开时拉取最近 50 条 IngestJob（左侧列表）
//   - 选中后右侧显示 Job 概要 + 文件级明细（按状态分组：失败/拦截/跳过/已落盘/待执行）
//   - 失败/拦截行展开看 SafetyNote / ErrorMsg + 源路径
//
// 设计：
//   - 单 Modal 含双栏（列表 + 详情），保持单屏体验；
//   - 不做分页（最多 50 条 + 单次明细），明细量大时用客户端虚拟滚动可后续加。

interface Props {
  isOpen: boolean
  onClose: () => void
}

export default function IngestHistoryModal({ isOpen, onClose }: Props) {
  const [jobs, setJobs] = useState<IngestJob[]>([])
  const [loading, setLoading] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [items, setItems] = useState<IngestJobItem[]>([])
  const [itemsLoading, setItemsLoading] = useState(false)
  const [itemsError, setItemsError] = useState('')

  const selected = useMemo(() => jobs.find((j) => j.id === selectedId) || null, [jobs, selectedId])

  const refreshJobs = async () => {
    setLoading(true)
    try {
      const resp = await ingestApi.listJobs(50)
      const list = resp.data?.data || []
      setJobs(list)
      // 默认选中第一条（如果当前 selectedId 不在列表中）
      if (list.length > 0 && !list.find((j) => j.id === selectedId)) {
        setSelectedId(list[0].id)
      }
    } catch (e) {
      console.warn('[IngestHistory] 拉取列表失败', e)
    } finally {
      setLoading(false)
    }
  }

  // 选中变化时拉明细
  useEffect(() => {
    if (!isOpen || !selectedId) return
    let cancelled = false
    setItemsLoading(true)
    setItemsError('')
    ingestApi
      .getJobItems(selectedId)
      .then((resp) => {
        if (cancelled) return
        setItems(resp.data?.data || [])
      })
      .catch((e: any) => {
        if (cancelled) return
        setItemsError(e?.response?.data?.error || e?.message || '加载明细失败')
      })
      .finally(() => {
        if (!cancelled) setItemsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId, isOpen])

  // 打开时刷新一次
  useEffect(() => {
    if (isOpen) refreshJobs()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isOpen])

  if (!isOpen) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div
        className="relative flex h-[85vh] w-full max-w-6xl flex-col overflow-hidden rounded-2xl shadow-2xl"
        style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}
      >
        {/* 顶栏 */}
        <div
          className="flex items-center justify-between border-b px-5 py-3"
          style={{ borderColor: 'var(--border-default)' }}
        >
          <div className="flex items-center gap-2">
            <History size={18} className="text-neon" />
            <h2 className="font-display text-lg font-semibold">入库历史</h2>
            <span className="ml-2 text-xs" style={{ color: 'var(--text-tertiary)' }}>
              共 {jobs.length} 条
            </span>
          </div>
          <div className="flex items-center gap-1">
            <button
              onClick={refreshJobs}
              disabled={loading}
              className="rounded-lg p-1.5 transition-colors hover:bg-white/5 disabled:opacity-50"
              title="刷新"
            >
              {loading ? (
                <Loader2 size={16} className="animate-spin" />
              ) : (
                <RotateCcw size={16} />
              )}
            </button>
            <button
              onClick={onClose}
              className="rounded-lg p-1.5 transition-colors hover:bg-white/5"
              title="关闭"
            >
              <X size={18} />
            </button>
          </div>
        </div>

        {/* 双栏 */}
        <div className="flex flex-1 min-h-0">
          {/* 左：任务列表 */}
          <div
            className="w-72 flex-shrink-0 overflow-y-auto border-r"
            style={{ borderColor: 'var(--border-default)' }}
          >
            {jobs.length === 0 && !loading && (
              <div className="p-6 text-center text-xs" style={{ color: 'var(--text-tertiary)' }}>
                暂无任务记录
              </div>
            )}
            {jobs.map((j) => (
              <JobListItem
                key={j.id}
                job={j}
                active={j.id === selectedId}
                onClick={() => setSelectedId(j.id)}
              />
            ))}
          </div>

          {/* 右：详情 */}
          <div className="flex-1 overflow-y-auto p-5">
            {!selected && (
              <div className="flex h-full items-center justify-center text-sm" style={{ color: 'var(--text-tertiary)' }}>
                选择一个任务查看明细
              </div>
            )}
            {selected && (
              <JobDetail
                job={selected}
                items={items}
                loading={itemsLoading}
                error={itemsError}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

// =====================================
// 左栏：任务列表项
// =====================================

function JobListItem({
  job,
  active,
  onClick,
}: {
  job: IngestJob
  active: boolean
  onClick: () => void
}) {
  const stats = parseIngestStats(job)
  const dot =
    job.status === 'completed'
      ? '#34d399'
      : job.status === 'failed'
      ? '#f43f5e'
      : job.status === 'canceled'
      ? '#94a3b8'
      : '#22d3ee'

  return (
    <button
      onClick={onClick}
      className="w-full border-b px-4 py-3 text-left transition-colors"
      style={{
        background: active ? 'var(--neon-tint)' : 'transparent',
        borderColor: 'var(--border-default)',
      }}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <span
            className="inline-block h-2 w-2 flex-shrink-0 rounded-full"
            style={{ background: dot }}
          />
          <span
            className="truncate text-xs font-medium"
            style={{ color: active ? 'var(--neon)' : 'var(--text-primary)' }}
            title={job.source_path}
          >
            {basename(job.source_path)}
          </span>
        </div>
        <ChevronRight size={14} style={{ color: 'var(--text-tertiary)' }} />
      </div>
      <div className="mt-1 flex items-center gap-2 text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
        <span>{statusLabel(job.status)}</span>
        <span>·</span>
        <span>{formatTime(job.created_at)}</span>
      </div>
      <div className="mt-1.5 text-[11px]" style={{ color: 'var(--text-secondary)' }}>
        ✅ {stats.executed} · ⏭ {stats.skipped} · ❌ {stats.failed}
        {stats.unsorted > 0 && ` · 🤔 ${stats.unsorted}`}
      </div>
    </button>
  )
}

// =====================================
// 右栏：任务详情 + 明细
// =====================================

function JobDetail({
  job,
  items,
  loading,
  error,
}: {
  job: IngestJob
  items: IngestJobItem[]
  loading: boolean
  error: string
}) {
  const stats = parseIngestStats(job)
  const libIds = parseLibraryIds(job)
  const grouped = useMemo(() => groupItemsByStatus(items), [items])

  // 渲染顺序：失败 → 拦截 → 跳过 → 已落盘 → 待执行
  const order: Array<{ key: string; label: string; emoji: string; danger?: boolean }> = [
    { key: 'failed', label: '失败', emoji: '❌', danger: true },
    { key: 'unsafe', label: '安全检测拦截', emoji: '🛑', danger: true },
    { key: 'skipped', label: '已跳过', emoji: '⏭' },
    { key: 'executed', label: '已落盘', emoji: '✅' },
    { key: 'pending', label: '待执行', emoji: '⏳' },
  ]

  return (
    <div className="space-y-4">
      {/* Header */}
      <div>
        <div className="flex items-center gap-2">
          <h3 className="font-display text-base font-semibold break-all">{basename(job.source_path)}</h3>
          <span
            className="rounded-full px-2 py-0.5 text-[11px]"
            style={{
              background: 'var(--bg-base)',
              color:
                job.status === 'completed'
                  ? '#34d399'
                  : job.status === 'failed'
                  ? '#f87171'
                  : 'var(--text-secondary)',
            }}
          >
            {statusLabel(job.status)}
          </span>
        </div>
        <p className="mt-1 break-all text-xs" style={{ color: 'var(--text-tertiary)' }}>
          {job.source_path} → {job.target_root}
        </p>
        <p className="mt-0.5 text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
          创建于 {formatTime(job.created_at)}
          {job.completed_at && ` · 完成于 ${formatTime(job.completed_at)}`}
        </p>
      </div>

      {/* 统计 */}
      <div className="grid grid-cols-7 gap-2">
        <StatTile label="扫描" value={stats.scanned} />
        <StatTile label="分类" value={stats.classified} />
        <StatTile label="规划" value={stats.planned} />
        <StatTile label="完成" value={stats.executed} accent="emerald" />
        <StatTile label="跳过" value={stats.skipped} />
        <StatTile label="失败" value={stats.failed} accent={stats.failed > 0 ? 'rose' : undefined} />
        <StatTile label="待人工" value={stats.unsorted} accent={stats.unsorted > 0 ? 'amber' : undefined} />
      </div>

      {/* 错误 */}
      {job.error_message && (
        <div
          className="flex items-start gap-2 rounded-lg px-3 py-2 text-xs"
          style={{ background: 'rgba(244,63,94,0.1)', color: '#fca5a5' }}
        >
          <AlertTriangle size={14} className="mt-0.5 flex-shrink-0" />
          <span className="break-all">{job.error_message}</span>
        </div>
      )}

      {/* 库 */}
      {libIds.length > 0 && (
        <div
          className="flex items-center gap-2 rounded-lg px-3 py-2 text-xs"
          style={{ background: 'rgba(16,185,129,0.1)', color: '#6ee7b7' }}
        >
          <CheckCircle2 size={14} className="flex-shrink-0" />
          <span>已建库 / 复用 {libIds.length} 个媒体库</span>
        </div>
      )}

      {/* 明细 */}
      <div>
        <div className="mb-2 flex items-center gap-2">
          <FileSearch size={14} style={{ color: 'var(--text-secondary)' }} />
          <span className="text-sm font-medium">文件明细</span>
          <span className="text-xs" style={{ color: 'var(--text-tertiary)' }}>
            （{items.length} 条）
          </span>
        </div>

        {loading && (
          <div className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-tertiary)' }}>
            <Loader2 size={14} className="animate-spin" />
            加载明细中…
          </div>
        )}
        {error && (
          <div
            className="flex items-start gap-2 rounded-lg px-3 py-2 text-xs"
            style={{ background: 'rgba(244,63,94,0.1)', color: '#fca5a5' }}
          >
            <AlertTriangle size={14} className="mt-0.5 flex-shrink-0" />
            <span>{error}</span>
          </div>
        )}
        {!loading && !error && items.length === 0 && (
          <div className="text-xs" style={{ color: 'var(--text-tertiary)' }}>
            该任务暂无文件明细
          </div>
        )}

        {/* 分组渲染 */}
        {!loading && !error && items.length > 0 && (
          <div className="space-y-3">
            {order.map(({ key, label, emoji, danger }) => {
              const list = grouped[key] || []
              if (list.length === 0) return null
              return (
                <ItemGroup key={key} label={`${emoji} ${label}`} count={list.length} danger={danger}>
                  {list.map((it) => (
                    <ItemRow key={it.id} item={it} />
                  ))}
                </ItemGroup>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}

// =====================================
// 子组件：分组容器 / 明细行 / 统计格
// =====================================

function ItemGroup({
  label,
  count,
  danger,
  children,
}: {
  label: string
  count: number
  danger?: boolean
  children: React.ReactNode
}) {
  const [open, setOpen] = useState(true)
  return (
    <div
      className="overflow-hidden rounded-lg border"
      style={{
        borderColor: danger ? 'rgba(244,63,94,0.4)' : 'var(--border-default)',
        background: 'var(--bg-base)',
      }}
    >
      <button
        onClick={() => setOpen(!open)}
        className="flex w-full items-center justify-between px-3 py-2 text-xs font-medium transition-colors hover:bg-white/5"
        style={{ color: danger ? '#fda4af' : 'var(--text-primary)' }}
      >
        <span>{label}</span>
        <span style={{ color: 'var(--text-tertiary)' }}>{count} 条 {open ? '▾' : '▸'}</span>
      </button>
      {open && <div className="divide-y" style={{ borderColor: 'var(--border-default)' }}>{children}</div>}
    </div>
  )
}

function ItemRow({ item }: { item: IngestJobItem }) {
  const danger = item.status === 'failed' || item.status === 'unsafe'
  const reason = item.error_msg || item.safety_note
  return (
    <div className="px-3 py-2 text-xs" style={{ color: 'var(--text-secondary)' }}>
      <div className="flex items-baseline gap-2">
        <span
          className="rounded px-1.5 py-0.5 text-[10px]"
          style={{
            background: danger ? 'rgba(244,63,94,0.15)' : 'rgba(255,255,255,0.05)',
            color: danger ? '#fca5a5' : 'var(--text-tertiary)',
          }}
        >
          {itemStatusLabel(item.status)}
        </span>
        <span className="break-all" style={{ color: 'var(--text-primary)' }}>
          {item.source_name || basename(item.source_path)}
        </span>
        {item.ai_invoked && (
          <span className="text-[10px]" style={{ color: 'var(--neon)' }} title="该条目调用了 AI 还原">
            AI
          </span>
        )}
        {typeof item.confidence === 'number' && (
          <span className="ml-auto text-[10px] tabular-nums" style={{ color: 'var(--text-tertiary)' }}>
            置信 {(item.confidence * 100).toFixed(0)}%
          </span>
        )}
      </div>
      {item.target_name && (
        <div className="mt-0.5 truncate text-[11px]" style={{ color: 'var(--text-tertiary)' }} title={item.target_path}>
          → {item.target_name}
        </div>
      )}
      {reason && (
        <div className="mt-1 break-all text-[11px]" style={{ color: danger ? '#fca5a5' : 'var(--text-tertiary)' }}>
          {reason}
        </div>
      )}
    </div>
  )
}

function StatTile({
  label,
  value,
  accent,
}: {
  label: string
  value: number
  accent?: 'emerald' | 'amber' | 'rose'
}) {
  const color =
    accent === 'emerald'
      ? '#34d399'
      : accent === 'amber'
      ? '#fbbf24'
      : accent === 'rose'
      ? '#f87171'
      : 'var(--text-primary)'
  return (
    <div className="rounded-lg px-2 py-1.5 text-center" style={{ background: 'var(--bg-base)' }}>
      <div className="text-base font-semibold tabular-nums" style={{ color }}>
        {value}
      </div>
      <div className="text-[10px]" style={{ color: 'var(--text-tertiary)' }}>
        {label}
      </div>
    </div>
  )
}

// =====================================
// 工具
// =====================================

function basename(p: string): string {
  if (!p) return ''
  const s = p.replace(/[\\/]+$/, '')
  const idx = Math.max(s.lastIndexOf('/'), s.lastIndexOf('\\'))
  return idx < 0 ? s : s.slice(idx + 1)
}

function formatTime(iso: string | null | undefined): string {
  if (!iso) return ''
  try {
    const d = new Date(iso)
    if (isNaN(d.getTime())) return iso
    return d.toLocaleString('zh-CN', { hour12: false })
  } catch {
    return iso
  }
}
