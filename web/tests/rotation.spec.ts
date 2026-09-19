import { expect, test } from '@playwright/test'

for (const width of [1440, 390]) {
  test(`轮换页面可配置三台机器并保留 Token（${width}）`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 })
    const accounts = [1, 2, 3].map(id => ({ id, remark: `轮换机器 ${id}`, instance_id: `i-${id}`, region_id: 'cn-hongkong', max_traffic: 200 }))
    let rotation = { enabled: false, hostname: 'proxy.example.com', token_configured: true, slots: [] as { account_id: number; start: string; end: string }[] }
    const state = { active_id: 0, ip: '', message: '', error: '', updated_at: '', retire_after: '' }
    const saves: Record<string, unknown>[] = []
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      switch (path) {
        case '/api/v1/system/init-status': return route.fulfill({ json: { initialized: true } })
        case '/api/v1/config': return route.fulfill({ json: { accounts, traffic_threshold: 95, timezone: 'Asia/Shanghai', enable_billing: false } })
        case '/api/v1/status': return route.fulfill({ json: { accounts: accounts.map(a => ({ id: a.id, account: `test-${a.id}`, remark: a.remark, region: a.region_id, region_name: '中国香港', flow_total: 200, flow_used: 10, percentage: 5, over_threshold: false, instance_status: 'Stopped', last_updated: new Date().toISOString(), stale: false, keepalive_paused: false })), system_last_run: new Date().toISOString() } })
        case '/api/v1/rotation':
          if (route.request().method() === 'PUT') {
            const body = route.request().postDataJSON()
            saves.push(body)
            rotation = { ...body, token_configured: true }
            delete (rotation as { token?: string }).token
          }
          return route.fulfill({ json: { config: rotation, state } })
        default: return route.fulfill({ json: [] })
      }
    })
    await page.goto('/#rotation')
    await expect(page.getByRole('heading', { name: '轮换运行' })).toBeVisible()
    await expect(page.getByPlaceholder('已保存，留空保留')).toHaveValue('')
    for (let i = 0; i < 3; i++) await page.getByRole('button', { name: '添加机器' }).click()
    await expect(page.getByRole('button', { name: '添加机器' })).toBeDisabled()
    await page.getByRole('button', { name: '平均分配时段' }).click()
    await expect(page.getByLabel('开始时间 2', { exact: true })).toHaveValue('08:00')
    await expect(page.getByLabel('结束时间 3', { exact: true })).toHaveValue('00:00')
    await page.getByRole('button', { name: '保存并启用' }).click()
    await expect(page.getByRole('button', { name: '停用轮换' })).toBeVisible()
    await expect(page.getByLabel('开始时间 1', { exact: true })).toBeDisabled()
    expect(saves[0].token).toBe('')
    expect(saves[0].slots).toHaveLength(3)
    await page.screenshot({ path: testInfo.outputPath(`rotation-${width}.png`), fullPage: true })
    const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
    expect(fits).toBe(true)
    await page.getByRole('button', { name: '停用轮换' }).click()
    await expect(page.getByRole('button', { name: '保存并启用' })).toBeVisible()
    await expect(page.getByLabel('开始时间 1', { exact: true })).toBeEnabled()
    await page.getByRole('button', { name: '返回实例' }).click()
    if (width < 768) await page.getByRole('button', { name: '菜单', exact: true }).click()
    await expect(page.getByRole('button', { name: '轮换运行' })).toBeVisible()
  })
}
