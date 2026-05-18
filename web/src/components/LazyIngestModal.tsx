import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ingestApi,
  isJobRunning,
  parseIngestStats,
  parseLibraryIds,
  statusLabel,
  type IngestJob,
} from '../api/ingest'
import { useWebSocket, WS_EVENTS } from '../hooks/useWebSocket'
import { Sparkles, FolderInput, X, Loader2, CheckCircle2, AlertTriangle, History } from 'lucide-react'
import IngestHistoryModal from './IngestHistoryModal'

// LazyIngestModal · 一键入库（懒人模式）
//
// 使用：
//   <LazyIngestModal isOpen={open} onClose={...} onCompleted={(libIds) => 触发列表刷新} />
//
// 行为：
//   1. 用户输入「源目录」（必填）；可选「目标根」（默认 = source/_organized）；
//   2. 点击开始 → 后端创建 IngestJob 并异步执行；
//   3. 前端轮询 GET /jobs/:id（每 1.5s）展示阶段、进度、统计；
//   4. 任务终态时高亮库 ID（点击可跳转到媒体库列表）。

interface Props {
  isOpen: boolean
  onClose: () => void
  /** 任务完成后回调，传入新建/复用的媒体库 ID 列表 */
  onCompleted?: (libraryIds: string[]) => void
}

export default function LazyIngestModal({ isOpen, onClose, onCompleted }: Props) {
  const [sourcePath, setSourcePath] = useState('')
  const [targetRoot, setTargetRoot] = useState('') // 空 = 让后端取默认（source/_organized）
  const [namingStyle, setNamingStyle] = useState<'jellyfin' | 'plex'>('jellyfin')
  const [submitting, setSubmitting] = useState(false)
  const [errorMsg, setErrorMsg] = useState('')
  const [job, setJob] = useState<IngestJob | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)

  const stats = useMemo(() => parseIngestStats(job), [job])
  const libIds = useMemo(() => parseLibraryIds(job), [job])
  const running = isJobRunning(job)

  // 使用全局 WS（只责责订阅/取消订阅，不重复建连）
  const { on, off } = useWebSocket()
  // 用 ref 存 jobId，避免订阅函数被重复创建/移除
  const jobIdRef = useRef<string | null>(null)
  useEffect(() => {
    jobIdRef.current = job?.id ?? null
  }, [job?.id])

  // WS 订阅 ingest_progress：推送的数据就是整个 IngestJob 对象
  useEffect(() => {
    const handler = (data: any) => {
      if (!data || typeof data !== 'object') return
      const incoming = data as IngestJob
      // 只采纳当前任务的推送
      if (!jobIdRef.current || incoming.id !== jobIdRef.current) return
      setJob(incoming)
      if (incoming.status === 'completed') {
        onCompleted?.(parseLibraryIds(incoming))
      }
    }
    on(WS_EVENTS.INGEST_PROGRESS, handler)
    return () => {
      off(WS_EVENTS.INGEST_PROGRESS, handler)
    }
  }, [on, off, onCompleted])

  // 关闭时重置（仅当不在运行）
  useEffect(() => {
    if (!isOpen) {
      // 不主动清 job：让用户下次打开时仍能看到上次结果（除非完全关闭后重启）
      setErrorMsg('')
    }
  }, [isOpen])

  // 轮询（兑底）：仅在 running 时启动，5s 一次。WS 在线时主推主、轮询作为保底
  useEffect(() => {
    if (!job || !running) return
    let stopped = false
    const tick = async () => {
      if (stopped) return
      try {
        const resp = await ingestApi.getJob(job.id)
        const next = resp.data?.data
        if (next) {
          setJob(next)
          if (!isJobRunning(next) && next.status === 'completed') {
            const ids = parseLibraryIds(next)
            onCompleted?.(ids)
          }
        }
      } catch (e) {
        // 单次失败不致命，继续轮询
        console.warn('[LazyIngest] 轮询失败', e)
      }
    }
    const timer = setInterval(tick, 5000)
    return () => {
      stopped = true
      clearInterval(timer)
    }
  }, [job?.id, running, onCompleted])

  if (!isOpen) return null

  const handleSubmit = async () => {
    setErrorMsg('')
    if (!sourcePath.trim()) {
      setErrorMsg('请填写源目录')
      return
    }
    setSubmitting(true)
    try {
      const resp = await ingestApi.submit({
        source_path: sourcePath.trim(),
        target_root: targetRoot.trim() || undefined,
        naming_style: namingStyle,
      })
      setJob(resp.data?.data || null)
    } catch (e: any) {
      setErrorMsg(e?.response?.data?.error || e?.message || '创建任务失败')
    } finally {
      setSubmitting(false)
    }
  }

  const handleCancel = async () => {
    if (!job) return
    try {
      await ingestApi.cancelJob(job.id)
      const resp = await ingestApi.getJob(job.id)
      setJob(resp.data?.data || null)
    } catch (e) {
      console.warn('[LazyIngest] 取消失败', e)
    }
  }

  const statusColor =
    job?.status === 'completed'
      ? 'text-emerald-400'
      : job?.status === 'failed'
      ? 'text-rose-400'
      : job?.status === 'canceled'
      ? 'text-zinc-400'
      : 'text-neon'

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div
        className="relative w-full max-w-2xl rounded-2xl p-6 shadow-2xl"
        style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}
      >
        {/* 关闭 */}
        <button
          className="absolute right-4 top-4 rounded-lg p-1.5 transition-colors hover:bg-white/5"
          onClick={onClose}
          disabled={running}
          title={running ? '任务运行中无法关闭' : '关闭'}
        >
          <X size={18} />
        </button>

        {/* 历史入口 */}
        <button
          className="absolute right-12 top-4 flex items-center gap-1 rounded-lg px-2 py-1 text-xs transition-colors hover:bg-white/5"
          onClick={() => setHistoryOpen(true)}
          style={{ color: 'var(--text-secondary)' }}
          title="查看历史任务与失败明细"
        >
          <History size={14} />
          历史
        </button>

        {/* 标题 */}
        <div className="mb-5 flex items-center gap-2">
          <Sparkles size={20} className="text-neon" />
          <h2 className="font-display text-xl font-semibold">一键入库 · AI 自动整理</h2>
        </div>

        <p className="mb-5 text-sm" style={{ color: 'var(--text-secondary)' }}>
          只需要给一个源目录，AI 会自动识别影视、按 Jellyfin/Emby 命名规则重组目录结构（仅硬链接，源文件 0 风险），然后自动建库 + 扫描。
        </p>

        {/* 表单 */}
        {!job && (
          <div className="space-y-3">
            <div>
              <label className="mb-1.5 block text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
                源目录 <span className="text-rose-400">*</span>
              </label>
              <input
                type="text"
                value={sourcePath}
                onChange={(e) => setSourcePath(e.target.value)}
                placeholder="例如：D:\Downloads\Movies"
                className="w-full rounded-lg px-3 py-2 text-sm outline-none transition-colors focus:border-neon"
                style={{
                  background: 'var(--bg-base)',
                  border: '1px solid var(--border-default)',
                  color: 'var(--text-primary)',
                }}
              />
            </div>

            <div>
              <label className="mb-1.5 block text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
                目标根目录 <span style={{ color: 'var(--text-tertiary)' }}>（可选）</span>
              </label>
              <input
                type="text"
                value={targetRoot}
                onChange={(e) => setTargetRoot(e.target.value)}
                placeholder="留空则使用 源目录/_organized"
                className="w-full rounded-lg px-3 py-2 text-sm outline-none transition-colors focus:border-neon"
                style={{
                  background: 'var(--bg-base)',
                  border: '1px solid var(--border-default)',
                  color: 'var(--text-primary)',
                }}
              />
              <p className="mt-1 text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
                仅使用硬链接：源文件 0 风险、瞬间完成、零额外空间。
                <span className="ml-1" style={{ color: '#fbbf24' }}>⚠ 目标根必须与源目录在同一卷</span>
                （否则任务会立即拒绝执行）。
              </p>
            </div>

            <div>
              <label className="mb-1.5 block text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
                命名风格
              </label>
              <div className="flex gap-2">
                {(['jellyfin', 'plex'] as const).map((s) => (
                  <button
                    key={s}
                    onClick={() => setNamingStyle(s)}
                    className="rounded-lg px-3 py-1.5 text-xs font-medium transition-all"
                    style={{
                      background: namingStyle === s ? 'var(--neon-tint)' : 'transparent',
                      border: `1px solid ${namingStyle === s ? 'var(--neon)' : 'var(--border-default)'}`,
                      color: namingStyle === s ? 'var(--neon)' : 'var(--text-secondary)',
                    }}
                  >
                    {s === 'jellyfin' ? 'Jellyfin/Emby [tmdbid-xxx]' : 'Plex {tmdb-xxx}'}
                  </button>
                ))}
              </div>
            </div>

            {errorMsg && (
              <div className="flex items-start gap-2 rounded-lg px-3 py-2 text-xs" style={{ background: 'rgba(244,63,94,0.1)', color: '#fca5a5' }}>
                <AlertTriangle size={14} className="mt-0.5 flex-shrink-0" />
                <span>{errorMsg}</span>
              </div>
            )}

            <div className="flex justify-end gap-2 pt-2">
              <button onClick={onClose} className="btn-ghost px-4 py-2 text-sm">
                取消
              </button>
              <button
                onClick={handleSubmit}
                disabled={submitting || !sourcePath.trim()}
                className="btn-primary gap-1.5 px-4 py-2 text-sm disabled:opacity-50"
              >
                {submitting ? <Loader2 size={14} className="animate-spin" /> : <FolderInput size={14} />}
                开始
              </button>
            </div>
          </div>
        )}

        {/* 进度 / 结果 */}
        {job && (
          <div className="space-y-4">
            {/* 阶段 + 进度条 */}
            <div>
              <div className="mb-1.5 flex items-baseline justify-between">
                <span className={`text-sm font-medium ${statusColor}`}>{statusLabel(job.status)}</span>
                <span className="text-xs" style={{ color: 'var(--text-tertiary)' }}>
                  {job.progress}%
                </span>
              </div>
              <div className="h-2 overflow-hidden rounded-full" style={{ background: 'var(--bg-base)' }}>
                <div
                  className="h-full transition-all duration-500"
                  style={{
                    width: `${job.progress}%`,
                    background: job.status === 'failed' ? '#f43f5e' : 'var(--neon)',
                  }}
                />
              </div>
              {job.phase && (
                <p className="mt-2 text-xs" style={{ color: 'var(--text-secondary)' }}>
                  {job.phase}
                </p>
              )}
            </div>

            {/* 统计卡片 */}
            <div className="grid grid-cols-4 gap-2">
              <StatCell label="扫描" value={stats.scanned} />
              <StatCell label="完成" value={stats.executed} accent="emerald" />
              <StatCell label="跳过" value={stats.skipped} />
              <StatCell label="待人工" value={stats.unsorted} accent={stats.unsorted > 0 ? 'amber' : undefined} />
            </div>

            {/* 路径概要 */}
            <div className="space-y-1 rounded-lg px-3 py-2 text-xs" style={{ background: 'var(--bg-base)' }}>
              <div className="flex gap-2">
                <span style={{ color: 'var(--text-tertiary)' }}>源</span>
                <span className="break-all" style={{ color: 'var(--text-primary)' }}>{job.source_path}</span>
              </div>
              <div className="flex gap-2">
                <span style={{ color: 'var(--text-tertiary)' }}>目标</span>
                <span className="break-all" style={{ color: 'var(--text-primary)' }}>{job.target_root}</span>
              </div>
            </div>

            {/* 错误 */}
            {job.error_message && (
              <div className="flex items-start gap-2 rounded-lg px-3 py-2 text-xs" style={{ background: 'rgba(244,63,94,0.1)', color: '#fca5a5' }}>
                <AlertTriangle size={14} className="mt-0.5 flex-shrink-0" />
                <span className="break-all">{job.error_message}</span>
              </div>
            )}

            {/* 完成后展示库 ID */}
            {job.status === 'completed' && libIds.length > 0 && (
              <div className="flex items-center gap-2 rounded-lg px-3 py-2 text-xs" style={{ background: 'rgba(16,185,129,0.1)', color: '#6ee7b7' }}>
                <CheckCircle2 size={14} className="flex-shrink-0" />
                <span>已建库（{libIds.length} 个），扫描已自动开始，可在媒体库列表查看进度。</span>
              </div>
            )}

            {/* 操作 */}
            <div className="flex justify-end gap-2 pt-2">
              {running ? (
                <button onClick={handleCancel} className="btn-ghost px-4 py-2 text-sm">
                  取消任务
                </button>
              ) : (
                <>
                  <button onClick={() => setJob(null)} className="btn-ghost px-4 py-2 text-sm">
                    再来一次
                  </button>
                  <button onClick={onClose} className="btn-primary px-4 py-2 text-sm">
                    完成
                  </button>
                </>
              )}
            </div>
          </div>
        )}
      </div>

      {/* 历史详情弹窗（叠在上层） */}
      <IngestHistoryModal isOpen={historyOpen} onClose={() => setHistoryOpen(false)} />
    </div>
  )
}

function StatCell({ label, value, accent }: { label: string; value: number; accent?: 'emerald' | 'amber' }) {
  const color =
    accent === 'emerald' ? '#34d399' : accent === 'amber' ? '#fbbf24' : 'var(--text-primary)'
  return (
    <div className="rounded-lg px-3 py-2 text-center" style={{ background: 'var(--bg-base)' }}>
      <div className="text-lg font-semibold tabular-nums" style={{ color }}>
        {value}
      </div>
      <div className="text-[11px]" style={{ color: 'var(--text-tertiary)' }}>
        {label}
      </div>
    </div>
  )
}
