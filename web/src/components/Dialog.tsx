import {
  createContext,
  useContext,
  useState,
  useCallback,
  useRef,
  useMemo,
  useEffect,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { motion, AnimatePresence } from 'framer-motion'
import {
  AlertTriangle,
  CheckCircle2,
  Info,
  XCircle,
  HelpCircle,
  X,
} from 'lucide-react'
import { modalOverlayVariants, modalContentVariants } from '@/lib/motion'

// ============================================================
// 全局 Dialog 系统 — 替代浏览器原生 confirm/alert/prompt
// 风格：深空霓虹 + glassmorphism（与 Toast 同语言）
// ============================================================

type Variant = 'default' | 'primary' | 'danger' | 'warning' | 'success' | 'error' | 'info'

interface BaseOptions {
  title?: string
  message?: ReactNode
  variant?: Variant
  /** 默认 true，为 false 则点击遮罩不关闭（仅按按钮） */
  dismissible?: boolean
}

interface ConfirmOptions extends BaseOptions {
  confirmText?: string
  cancelText?: string
}

interface AlertOptions extends BaseOptions {
  okText?: string
}

interface PromptOptions extends BaseOptions {
  defaultValue?: string
  placeholder?: string
  /** 输入类型：text | password | textarea */
  inputType?: 'text' | 'password' | 'textarea'
  confirmText?: string
  cancelText?: string
  /** 校验函数；返回字符串表示错误信息，返回空表示通过 */
  validator?: (value: string) => string | null | undefined
}

interface DialogContextType {
  confirm: (opts: ConfirmOptions) => Promise<boolean>
  alert: (opts: AlertOptions) => Promise<void>
  prompt: (opts: PromptOptions) => Promise<string | null>
}

const DialogContext = createContext<DialogContextType | null>(null)

export function useDialog() {
  const ctx = useContext(DialogContext)
  if (!ctx) throw new Error('useDialog must be used within <DialogProvider>')
  return ctx
}

// ──────────────────── 内部状态模型 ────────────────────

type DialogKind = 'confirm' | 'alert' | 'prompt'

interface DialogState {
  id: string
  kind: DialogKind
  options: ConfirmOptions | AlertOptions | PromptOptions
  resolve: (value: any) => void
}

// ──────────────────── 视觉映射 ────────────────────

const variantIconMap: Record<Variant, ReactNode> = {
  default: <HelpCircle size={22} style={{ color: 'var(--neon-blue)' }} />,
  primary: <Info size={22} style={{ color: 'var(--neon-blue)' }} />,
  info: <Info size={22} style={{ color: 'var(--neon-blue)' }} />,
  success: <CheckCircle2 size={22} style={{ color: 'var(--neon-green, #00ff88)' }} />,
  warning: <AlertTriangle size={22} className="text-yellow-400" />,
  danger: <AlertTriangle size={22} className="text-red-400" />,
  error: <XCircle size={22} className="text-red-400" />,
}

const variantBorderMap: Record<Variant, string> = {
  default: 'rgba(0, 240, 255, 0.18)',
  primary: 'rgba(0, 240, 255, 0.18)',
  info: 'rgba(0, 240, 255, 0.18)',
  success: 'rgba(0, 255, 136, 0.20)',
  warning: 'rgba(234, 179, 8, 0.20)',
  danger: 'rgba(239, 68, 68, 0.22)',
  error: 'rgba(239, 68, 68, 0.22)',
}

const variantBtnMap: Record<Variant, string> = {
  default:
    'bg-[var(--neon-blue)] hover:bg-[var(--neon-blue-80,#00d6f0)] text-black',
  primary:
    'bg-[var(--neon-blue)] hover:bg-[var(--neon-blue-80,#00d6f0)] text-black',
  info:
    'bg-[var(--neon-blue)] hover:bg-[var(--neon-blue-80,#00d6f0)] text-black',
  success:
    'bg-emerald-500 hover:bg-emerald-400 text-black',
  warning:
    'bg-yellow-500 hover:bg-yellow-400 text-black',
  danger:
    'bg-red-500 hover:bg-red-400 text-white',
  error:
    'bg-red-500 hover:bg-red-400 text-white',
}

// ──────────────────── Provider ────────────────────

export function DialogProvider({ children }: { children: ReactNode }) {
  const [stack, setStack] = useState<DialogState[]>([])
  const idRef = useRef(0)

  const push = useCallback((d: Omit<DialogState, 'id'>) => {
    const id = `dlg-${++idRef.current}`
    setStack((prev) => [...prev, { ...d, id }])
    return id
  }, [])

  const pop = useCallback((id: string) => {
    setStack((prev) => prev.filter((d) => d.id !== id))
  }, [])

  const confirm = useCallback(
    (options: ConfirmOptions) =>
      new Promise<boolean>((resolve) => {
        push({ kind: 'confirm', options, resolve })
      }),
    [push]
  )

  const alert = useCallback(
    (options: AlertOptions) =>
      new Promise<void>((resolve) => {
        push({ kind: 'alert', options, resolve: () => resolve() })
      }),
    [push]
  )

  const prompt = useCallback(
    (options: PromptOptions) =>
      new Promise<string | null>((resolve) => {
        push({ kind: 'prompt', options, resolve })
      }),
    [push]
  )

  const value = useMemo<DialogContextType>(
    () => ({ confirm, alert, prompt }),
    [confirm, alert, prompt]
  )

  return (
    <DialogContext.Provider value={value}>
      {children}
      {createPortal(
        <AnimatePresence>
          {stack.map((d) => (
            <DialogShell
              key={d.id}
              state={d}
              onClose={(result) => {
                d.resolve(result)
                pop(d.id)
              }}
            />
          ))}
        </AnimatePresence>,
        typeof document !== 'undefined' ? document.body : (null as any)
      )}
    </DialogContext.Provider>
  )
}

// ──────────────────── 弹窗主体 ────────────────────

function DialogShell({
  state,
  onClose,
}: {
  state: DialogState
  onClose: (result: any) => void
}) {
  const { kind, options } = state
  const variant: Variant = options.variant ?? (kind === 'confirm' ? 'default' : 'info')
  const dismissible = options.dismissible !== false

  // ESC 关闭（取消语义）
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && dismissible) {
        e.preventDefault()
        if (kind === 'confirm') onClose(false)
        else if (kind === 'prompt') onClose(null)
        else onClose(undefined)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [dismissible, kind, onClose])

  // 锁滚动（多层弹窗时叠加）
  useEffect(() => {
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [])

  return (
    <motion.div
      className="fixed inset-0 z-[1000] flex items-center justify-center p-4"
      variants={modalOverlayVariants}
      initial="initial"
      animate="animate"
      exit="exit"
      style={{
        background: 'rgba(0, 0, 0, 0.55)',
        backdropFilter: 'blur(6px)',
      }}
      onClick={() => {
        if (!dismissible) return
        if (kind === 'confirm') onClose(false)
        else if (kind === 'prompt') onClose(null)
        else onClose(undefined)
      }}
    >
      <motion.div
        variants={modalContentVariants}
        className="relative w-full max-w-md rounded-2xl shadow-2xl"
        style={{
          background: 'var(--bg-elevated)',
          border: `1px solid ${variantBorderMap[variant]}`,
          backdropFilter: 'blur(24px)',
        }}
        onClick={(e) => e.stopPropagation()}
      >
        {/* 关闭按钮（右上角） */}
        {dismissible && (
          <button
            type="button"
            aria-label="关闭"
            onClick={() => {
              if (kind === 'confirm') onClose(false)
              else if (kind === 'prompt') onClose(null)
              else onClose(undefined)
            }}
            className="absolute right-3 top-3 rounded-lg p-1.5 text-surface-500 transition-colors hover:bg-white/5 hover:text-white"
          >
            <X size={16} />
          </button>
        )}

        {kind === 'confirm' && (
          <ConfirmBody
            options={options as ConfirmOptions}
            variant={variant}
            onClose={onClose}
          />
        )}
        {kind === 'alert' && (
          <AlertBody
            options={options as AlertOptions}
            variant={variant}
            onClose={onClose}
          />
        )}
        {kind === 'prompt' && (
          <PromptBody
            options={options as PromptOptions}
            variant={variant}
            onClose={onClose}
          />
        )}
      </motion.div>
    </motion.div>
  )
}

// ──────────────────── ConfirmBody ────────────────────

function ConfirmBody({
  options,
  variant,
  onClose,
}: {
  options: ConfirmOptions
  variant: Variant
  onClose: (result: boolean) => void
}) {
  const confirmRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    confirmRef.current?.focus()
  }, [])

  return (
    <div className="px-6 pb-6 pt-7">
      <div className="flex items-start gap-3">
        <div className="shrink-0 mt-0.5">{variantIconMap[variant]}</div>
        <div className="flex-1 min-w-0">
          {options.title && (
            <h3
              className="text-base font-semibold leading-tight"
              style={{ color: 'var(--text-primary)' }}
            >
              {options.title}
            </h3>
          )}
          {options.message && (
            <div
              className={`text-sm leading-relaxed ${options.title ? 'mt-2' : ''}`}
              style={{ color: 'var(--text-secondary)' }}
            >
              {options.message}
            </div>
          )}
        </div>
      </div>

      <div className="mt-6 flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={() => onClose(false)}
          className="rounded-lg px-4 py-2 text-sm font-medium transition-colors hover:bg-white/5"
          style={{
            color: 'var(--text-secondary)',
            border: '1px solid rgba(255,255,255,0.08)',
          }}
        >
          {options.cancelText ?? '取消'}
        </button>
        <button
          ref={confirmRef}
          type="button"
          onClick={() => onClose(true)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onClose(true)
          }}
          className={`rounded-lg px-4 py-2 text-sm font-semibold transition-colors ${variantBtnMap[variant]}`}
        >
          {options.confirmText ?? '确定'}
        </button>
      </div>
    </div>
  )
}

// ──────────────────── AlertBody ────────────────────

function AlertBody({
  options,
  variant,
  onClose,
}: {
  options: AlertOptions
  variant: Variant
  onClose: (result: undefined) => void
}) {
  const okRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    okRef.current?.focus()
  }, [])

  return (
    <div className="px-6 pb-6 pt-7">
      <div className="flex items-start gap-3">
        <div className="shrink-0 mt-0.5">{variantIconMap[variant]}</div>
        <div className="flex-1 min-w-0">
          {options.title && (
            <h3
              className="text-base font-semibold leading-tight"
              style={{ color: 'var(--text-primary)' }}
            >
              {options.title}
            </h3>
          )}
          {options.message && (
            <div
              className={`text-sm leading-relaxed ${options.title ? 'mt-2' : ''}`}
              style={{ color: 'var(--text-secondary)' }}
            >
              {options.message}
            </div>
          )}
        </div>
      </div>

      <div className="mt-6 flex items-center justify-end">
        <button
          ref={okRef}
          type="button"
          onClick={() => onClose(undefined)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onClose(undefined)
          }}
          className={`rounded-lg px-4 py-2 text-sm font-semibold transition-colors ${variantBtnMap[variant]}`}
        >
          {options.okText ?? '知道了'}
        </button>
      </div>
    </div>
  )
}

// ──────────────────── PromptBody ────────────────────

function PromptBody({
  options,
  variant,
  onClose,
}: {
  options: PromptOptions
  variant: Variant
  onClose: (result: string | null) => void
}) {
  const [value, setValue] = useState(options.defaultValue ?? '')
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement | HTMLTextAreaElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select?.()
  }, [])

  const submit = useCallback(() => {
    if (options.validator) {
      const err = options.validator(value)
      if (err) {
        setError(err)
        return
      }
    }
    onClose(value)
  }, [value, options, onClose])

  const inputType = options.inputType ?? 'text'

  return (
    <div className="px-6 pb-6 pt-7">
      <div className="flex items-start gap-3">
        <div className="shrink-0 mt-0.5">{variantIconMap[variant]}</div>
        <div className="flex-1 min-w-0">
          {options.title && (
            <h3
              className="text-base font-semibold leading-tight"
              style={{ color: 'var(--text-primary)' }}
            >
              {options.title}
            </h3>
          )}
          {options.message && (
            <div
              className={`text-sm leading-relaxed ${options.title ? 'mt-2' : ''}`}
              style={{ color: 'var(--text-secondary)' }}
            >
              {options.message}
            </div>
          )}

          <div className="mt-4">
            {inputType === 'textarea' ? (
              <textarea
                ref={inputRef as React.RefObject<HTMLTextAreaElement>}
                value={value}
                onChange={(e) => {
                  setValue(e.target.value)
                  if (error) setError(null)
                }}
                placeholder={options.placeholder}
                rows={4}
                className="w-full rounded-lg px-3 py-2 text-sm outline-none transition-colors resize-y"
                style={{
                  background: 'rgba(255,255,255,0.04)',
                  border: '1px solid rgba(255,255,255,0.10)',
                  color: 'var(--text-primary)',
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) submit()
                }}
              />
            ) : (
              <input
                ref={inputRef as React.RefObject<HTMLInputElement>}
                type={inputType}
                value={value}
                onChange={(e) => {
                  setValue(e.target.value)
                  if (error) setError(null)
                }}
                placeholder={options.placeholder}
                className="w-full rounded-lg px-3 py-2 text-sm outline-none transition-colors"
                style={{
                  background: 'rgba(255,255,255,0.04)',
                  border: '1px solid rgba(255,255,255,0.10)',
                  color: 'var(--text-primary)',
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') submit()
                }}
              />
            )}
            {error && (
              <p className="mt-2 text-xs text-red-400">{error}</p>
            )}
          </div>
        </div>
      </div>

      <div className="mt-6 flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={() => onClose(null)}
          className="rounded-lg px-4 py-2 text-sm font-medium transition-colors hover:bg-white/5"
          style={{
            color: 'var(--text-secondary)',
            border: '1px solid rgba(255,255,255,0.08)',
          }}
        >
          {options.cancelText ?? '取消'}
        </button>
        <button
          type="button"
          onClick={submit}
          className={`rounded-lg px-4 py-2 text-sm font-semibold transition-colors ${variantBtnMap[variant]}`}
        >
          {options.confirmText ?? '确定'}
        </button>
      </div>
    </div>
  )
}

export default DialogProvider
