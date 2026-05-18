import api from './client'

// =====================================
// AICost · AI 模型选择 / 成本估算 API
// =====================================
//
// 对接后端 /api/admin/ai/{models,cost/*} 接口：
//   - 列出可选模型（按 provider 过滤）
//   - 单次调用估价（USD + CNY）
//   - 当前 AIService 累计 token 总花费

export interface AIModelInfo {
  provider: string
  id: string
  label: string
  context_length: number
  /** USD per 1K input tokens */
  input_price: number
  /** USD per 1K output tokens */
  output_price: number
  recommended?: boolean
  notes?: string
}

export interface CostEstimate {
  provider: string
  model: string
  prompt_tokens: number
  completion_tokens: number
  input_price: number
  output_price: number
  cost_usd: number
  cost_cny: number
  usd_to_cny_rate: number
  fallback?: boolean
  note?: string
}

export interface CostSummary {
  provider: string
  model: string
  monthly_calls: number
  total_prompt_tokens: number
  total_completion_tokens: number
  estimate: CostEstimate
  current_model_info?: AIModelInfo
}

export const aiCostApi = {
  /** 列出指定 provider 下的可选模型（provider 留空返回全部） */
  listModels: (provider?: string) =>
    api.get<{ data: AIModelInfo[]; total: number }>('/admin/ai/models', {
      params: provider ? { provider } : undefined,
    }),

  /**
   * 单次调用估价
   * @param model      模型 ID（必填）
   * @param promptTokens  默认 1000
   * @param completionTokens 默认 500
   * @param provider   可选（不传时按 model 自动匹配 catalog）
   */
  estimate: (model: string, promptTokens = 1000, completionTokens = 500, provider?: string) =>
    api.get<{ data: CostEstimate }>('/admin/ai/cost/estimate', {
      params: {
        model,
        prompt_tokens: promptTokens,
        completion_tokens: completionTokens,
        ...(provider ? { provider } : {}),
      },
    }),

  /** 累计花费汇总（基于当前 AIService 内存计数） */
  summary: () => api.get<{ data: CostSummary }>('/admin/ai/cost/summary'),
}

/** 把 USD 价格格式化为"美元/人民币"双显示（用于 UI 标签） */
export function formatCostDual(usd: number, cnyRate = 7.2): string {
  const cny = usd * cnyRate
  if (usd === 0) return '免费'
  if (usd < 0.01) return `$${usd.toFixed(5)} (¥${cny.toFixed(4)})`
  return `$${usd.toFixed(4)} (¥${cny.toFixed(3)})`
}

/** 把 model 单价格式化为"输入 / 输出 / 1K tokens" */
export function formatModelPrice(m: AIModelInfo): string {
  if (m.input_price === 0 && m.output_price === 0) return '免费'
  return `输入 $${m.input_price}/1K · 输出 $${m.output_price}/1K`
}
