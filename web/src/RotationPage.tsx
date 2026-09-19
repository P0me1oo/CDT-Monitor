import { useEffect, useState } from 'react'
import { api } from './api'
import type { Config, StatusResponse } from './types'

type Slot = { account_id: number; start: string; end: string }
type RotationConfig = { enabled: boolean; hostname: string; token?: string; token_configured: boolean; slots: Slot[] }
type RotationResponse = {
  config: RotationConfig
  state: { active_id: number; ip: string; retire_after: string; updated_at: string; message: string; error: string }
}

export default function RotationPage({ config, status, onBack }: { config: Config; status: StatusResponse; onBack: () => void }) {
  const [saved, setSaved] = useState<RotationResponse | null>(null)
  const [draft, setDraft] = useState<RotationConfig | null>(null)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [retry, setRetry] = useState(0)

  useEffect(() => {
    let disposed = false
    const load = async () => {
      try {
        const response = await api<RotationResponse>('/api/v1/rotation')
        if (!disposed) {
          setSaved(response)
          setDraft(current => current ?? { ...response.config, token: '' })
        }
      } catch (cause) { if (!disposed) setError(cause instanceof Error ? cause.message : '轮换配置加载失败') }
    }
    void load()
    const timer = window.setInterval(() => void load(), 15_000)
    return () => { disposed = true; window.clearInterval(timer) }
  }, [retry])

  const save = async (enabled: boolean) => {
    if (!draft || !saved) return
    setBusy(true); setError(''); setNotice('')
    try {
      const body = saved.config.enabled ? { ...saved.config, enabled: false } : { ...draft, enabled }
      const response = await api<RotationResponse>('/api/v1/rotation', { method: 'PUT', body: JSON.stringify(body) })
      setSaved(response); setDraft({ ...response.config, token: '' })
      setNotice(enabled ? '轮换已启用，等待调度执行' : saved.config.enabled ? '轮换已停用，机器保持当前状态' : '配置已保存')
    } catch (cause) { setError(cause instanceof Error ? cause.message : '保存失败') }
    finally { setBusy(false) }
  }
  const updateSlot = (index: number, values: Partial<Slot>) => {
    if (draft) setDraft({ ...draft, slots: draft.slots.map((slot, i) => i === index ? { ...slot, ...values } : slot) })
  }
  const add = () => {
    if (!draft) return
    const account = config.accounts.find(a => a.instance_id && !draft.slots.some(s => s.account_id === a.id))
    if (account) setDraft({ ...draft, slots: [...draft.slots, { account_id: account.id, start: '00:00', end: '00:00' }] })
  }
  const distribute = () => {
    if (!draft || draft.slots.length < 2) return
    const clock = (minutes: number) => `${String(Math.floor(minutes / 60) % 24).padStart(2, '0')}:${String(minutes % 60).padStart(2, '0')}`
    setDraft({ ...draft, slots: draft.slots.map((slot, i, slots) => ({ ...slot, start: clock(Math.floor(i * 1440 / slots.length)), end: clock(Math.floor((i + 1) * 1440 / slots.length)) })) })
  }
  const locked = busy || !!saved?.config.enabled
  const current = config.accounts.find(a => a.id === saved?.state.active_id)
  const clock = new Intl.DateTimeFormat('en-GB', { timeZone: config.timezone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(new Date())
  const ordered = [...(saved?.config.slots ?? [])].sort((a, b) => a.start.localeCompare(b.start))
  const nextSlot = ordered.find(s => s.start > clock) ?? ordered[0]
  const nextAccount = config.accounts.find(a => a.id === nextSlot?.account_id)

  return <main className="app-shell rotation-page">
    <header className="rotation-heading">
      <div><button className="button button--secondary" onClick={onBack}>返回实例</button><h1>轮换运行</h1></div>
      <span className="rotation-enabled">{saved?.config.enabled ? '已启用' : '未启用'}</span>
    </header>
    {error && <div className="inline-error" role="alert">{error}</div>}
    {notice && <p role="status">{notice}</p>}
    {!draft || !saved ? <section className="glass-card rotation-section"><p>{error ? '暂时无法读取配置' : '正在加载轮换配置'}</p>{error && <button className="button button--secondary" onClick={() => setRetry(v => v + 1)}>重试</button>}</section> : <>
      <section className="glass-card rotation-section" aria-label="轮换状态">
        <dl className="rotation-status">
          <div><dt>当前指向</dt><dd>{current ? current.remark || current.instance_id : '暂无'}</dd></div>
          <div><dt>公网 IP</dt><dd>{saved.state.ip || '暂无'}</dd></div>
          <div><dt>流量告警阈值</dt><dd>{config.traffic_threshold}%</dd></div>
          <div><dt>时区</dt><dd>{config.timezone}</dd></div>
        </dl>
        <p className="rotation-message" role="status">{saved.config.enabled ? saved.state.message || '等待调度' : '轮换已停用，机器保持当前状态，原定时与保活规则恢复生效。'}</p>
        {saved.config.enabled && nextSlot && <p className="rotation-message">下一计划：{nextSlot.start} · {nextAccount?.remark || nextAccount?.instance_id || '未知机器'}（超限时跳过）</p>}
        {saved.state.error && <p className="inline-error" role="alert">{saved.state.error}</p>}
      </section>
      <form onSubmit={event => { event.preventDefault(); void save(false) }}>
        <fieldset disabled={locked} className="rotation-fields">
          <section className="glass-card rotation-section">
            <h2>域名设置</h2>
            <div className="rotation-form-grid">
              <label>轮换域名<input required placeholder="proxy.example.com" value={draft.hostname} onChange={e => setDraft({ ...draft, hostname: e.target.value })} /></label>
              <label>Cloudflare API Token<input type="password" autoComplete="new-password" placeholder={draft.token_configured ? '已保存，留空保留' : '输入 Token'} value={draft.token || ''} onChange={e => setDraft({ ...draft, token: e.target.value })} /></label>
            </div>
            <p className="muted">Token 需要该域名的 Zone Read 和 DNS Edit 权限。使用灰云 A 记录，TTL 设为 60 秒。</p>
          </section>
          <section className="glass-card rotation-section">
            <div className="rotation-section-title"><h2>每日时间表</h2><button type="button" className="button button--secondary" disabled={draft.slots.length < 2} onClick={distribute}>平均分配时段</button></div>
            <p className="muted">时段需连续覆盖全天；结束时间早于开始时间表示跨天。超限机器立即节省停机，由下一台未超限机器提前接班。</p>
            <div className="rotation-slots">
              {draft.slots.map((slot, index) => {
                const summary = status.accounts.find(a => a.id === slot.account_id)
                return <div className="rotation-slot" key={index}>
                  <label>机器<select aria-label={`机器 ${index + 1}`} value={slot.account_id} onChange={e => updateSlot(index, { account_id: Number(e.target.value) })}>
                    {!config.accounts.some(a => a.id === slot.account_id) && <option value={slot.account_id}>机器已删除，请重新选择</option>}
                    {config.accounts.filter(a => a.instance_id).map(a => <option key={a.id} value={a.id} disabled={draft.slots.some((s, i) => i !== index && s.account_id === a.id)}>{a.remark || a.instance_id}（{a.region_id}）</option>)}
                  </select></label>
                  <label>开始<input aria-label={`开始时间 ${index + 1}`} type="time" required value={slot.start} onChange={e => updateSlot(index, { start: e.target.value })} /></label>
                  <label>结束<input aria-label={`结束时间 ${index + 1}`} type="time" required value={slot.end} onChange={e => updateSlot(index, { end: e.target.value })} /></label>
                  <span className={summary?.over_threshold ? 'rotation-over' : 'muted'}>{summary ? `${summary.percentage.toFixed(1)}%${summary.over_threshold ? ' · 已超限' : ''}` : '待查询'}</span>
                  <button type="button" className="button button--secondary" aria-label={`移除机器 ${index + 1}`} onClick={() => setDraft({ ...draft, slots: draft.slots.filter((_, i) => i !== index) })}>移除</button>
                </div>
              })}
            </div>
            {draft.slots.length === 0 && <p className="muted">添加至少两台机器，再设置各自时段。</p>}
            <button type="button" className="button button--secondary" disabled={!config.accounts.some(a => a.instance_id && !draft.slots.some(s => s.account_id === a.id))} onClick={add}>添加机器</button>
          </section>
        </fieldset>
        <footer className="rotation-footer">
          <p className="muted">启用后接管所选机器的定时与保活，统一使用节省停机。灰云切换时，旧连接仍可能中断。</p>
          <div>{saved.config.enabled ? <button type="button" className="button button--secondary" disabled={busy} onClick={() => void save(false)}>停用轮换</button> : <>
            <button type="submit" className="button button--secondary" disabled={busy}>保存配置</button>
            <button type="button" className="button button--primary" disabled={busy || draft.slots.length < 2 || !draft.hostname} onClick={() => void save(true)}>{busy ? '正在保存' : '保存并启用'}</button>
          </>}</div>
        </footer>
      </form>
    </>}
  </main>
}
