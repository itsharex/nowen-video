import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import toast from 'react-hot-toast'
import { smartRenameApi, type RenamePlan } from '@/api/smart_rename'
import { pageVariants } from '@/lib/motion'
import SmartRenamePanel, { planStatusBadge } from '@/components/SmartRenamePanel'
import { useDialog } from '@/components/Dialog'

// =============================================================
// SmartRenamePage 智能扫描重命名（独立页）
// =============================================================
// 复用 SmartRenamePanel 作为核心交互区，本页额外提供"历史规划"列表，
// 用于管理员翻看 / 复盘 / 删除过往规划。媒体库行内入口走 SmartRenameDrawer，
// 同样复用 Panel；二者保持单一来源。

const cardClass = 'rounded-xl p-5'
const cardStyle: React.CSSProperties = {
  background: 'var(--bg-card)',
  border: '1px solid var(--border-default)',
  boxShadow: 'var(--shadow-card)',
}

export default function SmartRenamePage() {
  const dialog = useDialog()
  const [planList, setPlanList] = useState<RenamePlan[]>([])
  const [planListLoading, setPlanListLoading] = useState(false)
  // 用于在选择历史规划后把它注入 Panel；通过 key 强制 Panel 重置内部状态。
  const [, setLoadedPlan] = useState<RenamePlan | null>(null)
  const [panelKey, setPanelKey] = useState(0)
  const [panelDefaultPath, setPanelDefaultPath] = useState('')

  useEffect(() => {
    loadHistory()
  }, [])

  async function loadHistory() {
    setPlanListLoading(true)
    try {
      const resp = await smartRenameApi.listPlans(1, 20)
      setPlanList(resp.data.data.items)
    } catch (e: any) {
      // eslint-disable-next-line no-console
      console.warn('加载历史失败', e)
    } finally {
      setPlanListLoading(false)
    }
  }

  async function onLoadPlan(id: string) {
    try {
      const resp = await smartRenameApi.getPlan(id)
      // 通过更换 key 重新挂载 Panel，并把根目录注入进去；
      // Panel 内部会以 defaultPath 触发重置，详情区按用户重新点"开始扫描"或继续后续操作。
      setLoadedPlan(resp.data.data)
      setPanelDefaultPath(resp.data.data.root_path)
      setPanelKey((k) => k + 1)
      toast.success(`已切换到历史规划 ${id.slice(0, 8)}…，可重新扫描或基于此根目录继续`)
    } catch (e: any) {
      toast.error(`加载失败：${e?.response?.data?.error || e.message}`)
    }
  }

  async function onDeletePlan(id: string) {
    const ok = await dialog.confirm({
      title: '删除规划记录',
      message: '删除该规划记录（不影响已落盘的文件）？',
      confirmText: '删除',
      variant: 'danger',
    })
    if (!ok) return
    try {
      await smartRenameApi.deletePlan(id)
      toast.success('已删除')
      loadHistory()
    } catch (e: any) {
      toast.error(`删除失败：${e?.response?.data?.error || e.message}`)
    }
  }

  return (
    <motion.div
      variants={pageVariants}
      initial="initial"
      animate="enter"
      exit="exit"
      className="w-full px-6 py-6"
      style={{ color: 'var(--text-primary)' }}
    >
      {/* ===== 核心交互面板 ===== */}
      <SmartRenamePanel
        key={panelKey}
        defaultPath={panelDefaultPath}
        showHeader
        onPlanChange={() => loadHistory()}
      />

      {/* ===== 历史规划 ===== */}
      <section className={cardClass} style={cardStyle}>
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>③ 历史规划</h2>
          <button
            onClick={loadHistory}
            disabled={planListLoading}
            className="text-xs transition"
            style={{ color: 'var(--text-tertiary)' }}
          >
            {planListLoading ? '加载中…' : '刷新'}
          </button>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead style={{ color: 'var(--text-tertiary)' }}>
              <tr style={{ borderBottom: '1px solid var(--border-default)' }}>
                <th className="px-2 py-2 text-left">创建时间</th>
                <th className="px-2 py-2 text-left">根目录</th>
                <th className="px-2 py-2 text-left">风格</th>
                <th className="px-2 py-2 text-left">状态</th>
                <th className="px-2 py-2 text-right">总/需改/已落盘</th>
                <th className="px-2 py-2 text-right">操作</th>
              </tr>
            </thead>
            <tbody>
              {planList.map((p) => (
                <tr
                  key={p.id}
                  style={{ borderBottom: '1px solid var(--border-subtle)' }}
                  className="hover:bg-[var(--nav-hover-bg)]"
                >
                  <td className="px-2 py-2" style={{ color: 'var(--text-secondary)' }}>
                    {new Date(p.created_at).toLocaleString()}
                  </td>
                  <td className="px-2 py-2 font-mono" style={{ color: 'var(--text-primary)' }}>
                    {p.root_path}
                  </td>
                  <td className="px-2 py-2" style={{ color: 'var(--text-secondary)' }}>
                    {p.naming_style}
                  </td>
                  <td className="px-2 py-2">
                    <span className={`rounded px-1.5 py-0.5 ${planStatusBadge(p.status)}`}>{p.status}</span>
                  </td>
                  <td className="px-2 py-2 text-right" style={{ color: 'var(--text-secondary)' }}>
                    {p.total_items} / {p.need_rename} / {p.executed_items}
                  </td>
                  <td className="px-2 py-2 text-right">
                    <button
                      onClick={() => onLoadPlan(p.id)}
                      className="mr-2 hover:underline"
                      style={{ color: 'var(--neon-blue)' }}
                    >
                      载入
                    </button>
                    <button
                      onClick={() => onDeletePlan(p.id)}
                      className="hover:underline text-rose-600 dark:text-rose-300"
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
              {planList.length === 0 && !planListLoading && (
                <tr>
                  <td
                    colSpan={6}
                    className="px-2 py-6 text-center"
                    style={{ color: 'var(--text-tertiary)' }}
                  >
                    暂无历史
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </motion.div>
  )
}
