import { useState, useEffect } from 'react'
import { userProfileApi } from '@/api'
import type { UserProfile } from '@/types'
import {
  Users, Plus, Shield, Baby, User, Pencil, Trash2, Eye,
  Clock, BarChart3, Loader2, X, Lock,
} from 'lucide-react'
import clsx from 'clsx'
import { useDialog } from '@/components/Dialog'

export default function UserProfileManager() {
  const dialog = useDialog()
  const [profiles, setProfiles] = useState<UserProfile[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [selectedProfile, setSelectedProfile] = useState<UserProfile | null>(null)
  const [showWatchLogs, setShowWatchLogs] = useState(false)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  const loadProfiles = async () => {
    try {
      const res = await userProfileApi.list()
      setProfiles(res.data.data || [])
    } catch { /* ignore */ } finally {
      setLoading(false)
    }
  }

  useEffect(() => { loadProfiles() }, [])

  const handleDelete = async (id: string) => {
    const ok = await dialog.confirm({
      title: '删除配置文件',
      message: '确定要删除此配置文件吗？',
      confirmText: '删除',
      variant: 'danger',
    })
    if (!ok) return
    try {
      await userProfileApi.delete(id)
      setMessage({ type: 'success', text: '配置文件已删除' })
      loadProfiles()
    } catch (err: any) {
      setMessage({ type: 'error', text: err.response?.data?.error || '删除失败' })
    }
    setTimeout(() => setMessage(null), 3000)
  }

  const handleSwitch = async (profile: UserProfile) => {
    let pin: string | undefined
    if (profile.pin) {
      const result = await dialog.prompt({
        title: '请输入 PIN 码',
        message: `切换到 ${profile.name} 需要 PIN 验证`,
        placeholder: 'PIN 码',
        inputType: 'password',
      })
      if (!result) return
      pin = result
    }
    try {
      await userProfileApi.switch(profile.id, pin)
      setMessage({ type: 'success', text: `已切换到 ${profile.name}` })
    } catch (err: any) {
      setMessage({ type: 'error', text: err.response?.data?.error || 'PIN 码错误' })
    }
    setTimeout(() => setMessage(null), 3000)
  }

  const getProfileIcon = (type: string) => {
    switch (type) {
      case 'kids': return <Baby className="h-6 w-6 text-green-400" />
      case 'restricted': return <Shield className="h-6 w-6 text-yellow-400" />
      default: return <User className="h-6 w-6 text-neon-blue" />
    }
  }

  const getProfileLabel = (type: string) => {
    switch (type) {
      case 'kids': return '儿童模式'
      case 'restricted': return '受限模式'
      default: return '标准模式'
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center py-20">
        <Loader2 className="h-8 w-8 animate-spin text-neon-blue" />
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {/* 消息提示 */}
      {message && (
        <div className={clsx(
          'rounded-xl px-4 py-3 text-sm font-medium',
          message.type === 'success' ? 'bg-green-500/10 text-green-400 border border-green-500/20' : 'bg-red-500/10 text-red-400 border border-red-500/20'
        )}>
          {message.text}
        </div>
      )}

      {/* 标题 */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Users className="h-5 w-5 text-neon-blue" />
          <h2 className="font-display text-xl font-semibold text-white">用户配置文件</h2>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="btn-neon rounded-xl px-4 py-2 text-sm font-medium flex items-center gap-2">
          <Plus className="h-4 w-4" /> 新建配置
        </button>
      </div>

      {/* 配置文件列表 */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {profiles.map(profile => (
          <div key={profile.id} className="card-glass rounded-2xl p-5 hover:border-neon-blue/30 transition-all group">
            <div className="flex items-start justify-between mb-4">
              <div className="flex items-center gap-3">
                <div className="w-12 h-12 rounded-xl bg-surface-800 flex items-center justify-center">
                  {getProfileIcon(profile.type)}
                </div>
                <div>
                  <h3 className="font-display text-base font-semibold text-white">{profile.name}</h3>
                  <span className={clsx(
                    'text-xs rounded-full px-2 py-0.5',
                    profile.type === 'kids' ? 'bg-green-500/20 text-green-400' :
                    profile.type === 'restricted' ? 'bg-yellow-500/20 text-yellow-400' :
                    'bg-neon-blue/20 text-neon-blue'
                  )}>
                    {getProfileLabel(profile.type)}
                  </span>
                </div>
              </div>
              {profile.pin && <Lock className="h-4 w-4 text-surface-500" />}
            </div>

            {/* 儿童模式信息 */}
            {profile.type === 'kids' && profile.kids_settings && (
              <div className="text-xs text-surface-400 space-y-1 mb-3">
                <p>每日限时: {profile.kids_settings.daily_time_limit_min} 分钟</p>
                <p>内容分级: {profile.kids_settings.max_content_rating || '全年龄'}</p>
                {profile.kids_settings.bedtime_start && (
                  <p>就寝时间: {profile.kids_settings.bedtime_start} - {profile.kids_settings.bedtime_end}</p>
                )}
              </div>
            )}

            {/* 操作按钮 */}
            <div className="flex items-center gap-2 mt-3 pt-3 border-t border-white/5">
              <button onClick={() => handleSwitch(profile)}
                className="btn-ghost flex-1 rounded-lg py-2 text-xs flex items-center justify-center gap-1">
                <User className="h-3 w-3" /> 切换
              </button>
              <button onClick={() => { setSelectedProfile(profile); setShowWatchLogs(true) }}
                className="btn-ghost rounded-lg px-3 py-2 text-xs">
                <Eye className="h-3 w-3" />
              </button>
              <button onClick={() => setSelectedProfile(profile)}
                className="btn-ghost rounded-lg px-3 py-2 text-xs">
                <Pencil className="h-3 w-3" />
              </button>
              {!profile.is_default && (
                <button onClick={() => handleDelete(profile.id)}
                  className="text-red-400 hover:text-red-300 rounded-lg px-3 py-2 text-xs">
                  <Trash2 className="h-3 w-3" />
                </button>
              )}
            </div>
          </div>
        ))}
      </div>

      {profiles.length === 0 && (
        <div className="text-center py-16 text-surface-500">
          <Users className="h-16 w-16 mx-auto mb-4 opacity-20" />
          <p className="text-lg">暂无配置文件</p>
          <p className="text-sm mt-1">创建配置文件以管理不同用户的观看体验</p>
        </div>
      )}

      {/* 创建配置文件弹窗 */}
      {showCreate && (
        <CreateProfileModal
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); loadProfiles() }}
        />
      )}

      {/* 观看日志弹窗 */}
      {showWatchLogs && selectedProfile && (
        <WatchLogsModal
          profile={selectedProfile}
          onClose={() => { setShowWatchLogs(false); setSelectedProfile(null) }}
        />
      )}
    </div>
  )
}

// 创建配置文件弹窗
function CreateProfileModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [type, setType] = useState<'standard' | 'kids' | 'restricted'>('standard')
  const [pin, setPin] = useState('')
  const [dailyLimit, setDailyLimit] = useState(120)
  const [maxRating, setMaxRating] = useState('G')
  const [saving, setSaving] = useState(false)

  const handleCreate = async () => {
    if (!name.trim()) return
    setSaving(true)
    try {
      const profile: Partial<UserProfile> = {
        name: name.trim(),
        type,
        pin: pin || '',
      }
      if (type === 'kids') {
        profile.kids_settings = {
          max_content_rating: maxRating,
          allowed_genres: [],
          blocked_genres: [],
          daily_time_limit_min: dailyLimit,
          bedtime_start: '21:00',
          bedtime_end: '07:00',
          require_approval: true,
        }
      }
      await userProfileApi.create(profile)
      onCreated()
    } catch { /* ignore */ } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="w-full max-w-md rounded-2xl p-6" style={{
        background: 'var(--glass-bg)', border: '1px solid var(--neon-blue-15)', backdropFilter: 'blur(20px)',
      }}>
        <div className="flex items-center justify-between mb-6">
          <h3 className="font-display text-lg font-semibold text-white">新建配置文件</h3>
          <button onClick={onClose} className="text-surface-400 hover:text-white"><X className="h-5 w-5" /></button>
        </div>

        <div className="space-y-4">
          <div>
            <label className="text-xs text-surface-400">名称</label>
            <input type="text" value={name} onChange={e => setName(e.target.value)}
              placeholder="输入配置文件名称" className="input-glass mt-1 w-full rounded-lg px-3 py-2 text-sm" />
          </div>

          <div>
            <label className="text-xs text-surface-400">类型</label>
            <div className="grid grid-cols-3 gap-2 mt-1">
              {[
                { key: 'standard' as const, label: '标准', icon: User },
                { key: 'kids' as const, label: '儿童', icon: Baby },
                { key: 'restricted' as const, label: '受限', icon: Shield },
              ].map(({ key, label, icon: Icon }) => (
                <button key={key} onClick={() => setType(key)}
                  className={clsx(
                    'flex flex-col items-center gap-1 rounded-xl py-3 text-xs transition-all',
                    type === key ? 'bg-neon-blue/20 text-neon-blue border border-neon-blue/30' : 'bg-surface-800/50 text-surface-400 border border-transparent hover:bg-surface-800'
                  )}>
                  <Icon className="h-5 w-5" />
                  {label}
                </button>
              ))}
            </div>
          </div>

          <div>
            <label className="text-xs text-surface-400">PIN 码（可选）</label>
            <input type="password" value={pin} onChange={e => setPin(e.target.value)}
              placeholder="设置 4-6 位 PIN 码" maxLength={6}
              className="input-glass mt-1 w-full rounded-lg px-3 py-2 text-sm" />
          </div>

          {type === 'kids' && (
            <>
              <div>
                <label className="text-xs text-surface-400">每日观看限时（分钟）</label>
                <input type="number" value={dailyLimit} onChange={e => setDailyLimit(parseInt(e.target.value) || 0)}
                  className="input-glass mt-1 w-full rounded-lg px-3 py-2 text-sm" />
              </div>
              <div>
                <label className="text-xs text-surface-400">最高内容分级</label>
                <select value={maxRating} onChange={e => setMaxRating(e.target.value)}
                  className="input-glass mt-1 w-full rounded-lg px-3 py-2 text-sm">
                  <option value="G">G - 全年龄</option>
                  <option value="PG">PG - 家长指导</option>
                  <option value="PG-13">PG-13 - 13岁以上</option>
                </select>
              </div>
            </>
          )}

          <div className="flex gap-3 pt-2">
            <button onClick={onClose} className="btn-ghost flex-1 rounded-xl py-2.5 text-sm">取消</button>
            <button onClick={handleCreate} disabled={saving || !name.trim()}
              className="btn-neon flex-1 rounded-xl py-2.5 text-sm font-medium flex items-center justify-center gap-2">
              {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
              创建
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

// 观看日志弹窗
function WatchLogsModal({ profile, onClose }: { profile: UserProfile; onClose: () => void }) {
  const [logs, setLogs] = useState<import('@/types').ProfileWatchLog[]>([])
  const [usage, setUsage] = useState<import('@/types').ProfileDailyUsage[]>([])
  const [loading, setLoading] = useState(true)
  const [tab, setTab] = useState<'logs' | 'usage'>('logs')

  useEffect(() => {
    Promise.all([
      userProfileApi.getWatchLogs(profile.id, 7),
      userProfileApi.getDailyUsage(profile.id, 14),
    ]).then(([logsRes, usageRes]) => {
      setLogs(logsRes.data.data || [])
      setUsage(usageRes.data.data || [])
    }).finally(() => setLoading(false))
  }, [profile.id])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="w-full max-w-lg max-h-[80vh] flex flex-col rounded-2xl" style={{
        background: 'var(--glass-bg)', border: '1px solid var(--neon-blue-15)', backdropFilter: 'blur(20px)',
      }}>
        <div className="flex items-center justify-between p-6 pb-0">
          <div>
            <h3 className="font-display text-lg font-semibold text-white">{profile.name} - 观看记录</h3>
            <p className="text-xs text-surface-400">{profile.type === 'kids' ? '儿童模式' : '标准模式'}</p>
          </div>
          <button onClick={onClose} className="text-surface-400 hover:text-white"><X className="h-5 w-5" /></button>
        </div>

        <div className="flex gap-1 mx-6 mt-4 rounded-xl bg-surface-800/50 p-1">
          {[
            { key: 'logs' as const, label: '观看日志', icon: Eye },
            { key: 'usage' as const, label: '使用统计', icon: BarChart3 },
          ].map(({ key, label, icon: Icon }) => (
            <button key={key} onClick={() => setTab(key)}
              className={clsx(
                'flex-1 flex items-center justify-center gap-2 rounded-lg py-2 text-sm font-medium transition-all',
                tab === key ? 'bg-neon-blue/20 text-neon-blue' : 'text-surface-400 hover:text-white'
              )}>
              <Icon className="h-4 w-4" /> {label}
            </button>
          ))}
        </div>

        <div className="flex-1 overflow-y-auto p-6 space-y-2">
          {loading ? (
            <div className="flex justify-center py-8"><Loader2 className="h-6 w-6 animate-spin text-neon-blue" /></div>
          ) : tab === 'logs' ? (
            logs.length > 0 ? logs.map(log => (
              <div key={log.id} className="flex items-center justify-between rounded-xl bg-surface-800/50 p-3">
                <div>
                  <p className="text-sm text-white">{log.media_title}</p>
                  <p className="text-xs text-surface-400">{new Date(log.started_at).toLocaleString()}</p>
                </div>
                <div className="flex items-center gap-1 text-xs text-surface-400">
                  <Clock className="h-3 w-3" />
                  {log.duration_min} 分钟
                </div>
              </div>
            )) : (
              <p className="text-center text-sm text-surface-500 py-8">暂无观看记录</p>
            )
          ) : (
            usage.length > 0 ? usage.map(u => (
              <div key={u.id} className="flex items-center justify-between rounded-xl bg-surface-800/50 p-3">
                <span className="text-sm text-white">{u.date}</span>
                <div className="flex items-center gap-4 text-xs text-surface-400">
                  <span>{u.media_count} 部</span>
                  <span className="font-medium text-neon-blue">{u.total_minutes} 分钟</span>
                </div>
              </div>
            )) : (
              <p className="text-center text-sm text-surface-500 py-8">暂无使用数据</p>
            )
          )}
        </div>
      </div>
    </div>
  )
}
