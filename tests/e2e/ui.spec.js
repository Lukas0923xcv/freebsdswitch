// @ts-check
const { test, expect } = require('@playwright/test')
const fs = require('fs')
const path = require('path')

const NEW_PW = 'e2e-Secret-Phrase-42'

function initialPassword() {
  if (process.env.BSDSWITCH_E2E_PASSWORD) return process.env.BSDSWITCH_E2E_PASSWORD
  return fs.readFileSync(path.join(__dirname, '.state/db/initial-admin-password'), 'utf8').trim()
}

async function login(page, pw, wait = true) {
  await page.goto('/')
  await page.fill('#u', 'admin')
  await page.fill('#p', pw)
  await page.click('button[type=submit]')
  if (wait) await expect(page.locator('.chassis')).toBeVisible()
}

test.describe.configure({ mode: 'serial' })

test('first login forces a password change', async ({ page }) => {
  await login(page, 'wrong password', false)
  await expect(page.getByRole('alert')).toContainText('invalid')
  await login(page, initialPassword(), false)
  await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible()
  await page.fill('#o', initialPassword())
  await page.fill('#n', NEW_PW)
  await page.fill('#n2', NEW_PW)
  await page.click('button[type=submit]')
  await expect(page.locator('.chassis')).toBeVisible()
  // the front panel shows every switch port
  await expect(page.locator('.chassis [data-port]')).toHaveCount(6)
})

test('VLAN matrix change, commit-confirm auto-rollback, apply+confirm, save', async ({ page }) => {
  await login(page, NEW_PW)
  await expect(page.locator('.chassis')).toBeVisible()

  // create VLAN 20
  await page.goto('/vlans')
  await page.getByRole('tab', { name: 'VLAN list' }).click()
  await page.getByTestId('new-vlan-id').fill('20')
  await page.getByTestId('new-vlan-name').fill('iot')
  await page.getByTestId('create-vlan').click()
  await expect(page.getByTestId('pending-bar')).toBeVisible()

  // matrix: vtnet2 not-a-member -> untagged in VLAN 20 (moves its PVID)
  await page.getByRole('tab', { name: 'Membership matrix' }).click()
  const cell = page.locator('[data-cell="vtnet2/20"]')
  await expect(cell).toHaveText('·')
  await cell.click()
  await expect(cell).toHaveText('U')
  await expect(page.locator('[data-cell="vtnet2/1"]')).toHaveText('·')
  await expect(cell).toHaveClass(/changed/)

  // review shows the exact command
  await page.getByTestId('review').click()
  await expect(page.getByTestId('commands')).toContainText('ifconfig bridge0 ifuntagged vtnet2 20')
  await page.getByRole('button', { name: 'Close' }).first().click()

  // apply with a 10 s confirm timeout and let it roll back
  await page.getByTestId('apply').click()
  await page.getByTestId('commit-timeout').selectOption('10')
  await page.getByTestId('apply-confirm').click()
  await expect(page.getByTestId('commit-banner')).toBeVisible()
  await expect(page.getByTestId('countdown')).toHaveText(/\d+s/)
  // kernel state while pending: vtnet2 is in VLAN 20
  let ports = await (await page.request.get('/api/v1/ports')).json()
  expect(ports.find((p) => p.name === 'vtnet2').kernel_pvid).toBe(20)

  await expect(page.getByText('was not confirmed and has been rolled back')).toBeVisible({ timeout: 20_000 })
  await expect(page.getByTestId('commit-banner')).toBeHidden()
  ports = await (await page.request.get('/api/v1/ports')).json()
  expect(ports.find((p) => p.name === 'vtnet2').kernel_pvid).toBe(1)
  // the rollback also restored the configuration: VLAN 20 is gone again
  await page.goto('/vlans')
  await expect(page.locator('[data-cell="vtnet2/20"]')).toHaveCount(0)

  // do it again, this time confirm
  await page.getByRole('tab', { name: 'VLAN list' }).click()
  await page.getByTestId('new-vlan-id').fill('20')
  await page.getByTestId('new-vlan-name').fill('iot')
  await page.getByTestId('create-vlan').click()
  await page.getByRole('tab', { name: 'Membership matrix' }).click()
  await page.locator('[data-cell="vtnet2/20"]').click()
  await expect(page.locator('[data-cell="vtnet2/20"]')).toHaveText('U')
  // and tag VLAN 20 on the uplink vtnet4 (access -> trunk, native 1)
  await page.locator('[data-cell="vtnet4/20"]').click()
  await page.locator('[data-cell="vtnet4/20"]').click()
  await expect(page.locator('[data-cell="vtnet4/20"]')).toHaveText('T')
  // the first click moved the untagged VLAN to 20; make VLAN 1 native again
  await expect(page.locator('[data-cell="vtnet4/1"]')).toHaveText('·')
  await page.locator('[data-cell="vtnet4/1"]').click()
  await expect(page.locator('[data-cell="vtnet4/1"]')).toHaveText('U')
  await expect(page.locator('[data-cell="vtnet4/20"]')).toHaveText('T')
  await expect(page.getByTestId('pending-count')).toContainText('pending change')

  await page.getByTestId('apply').click()
  await page.getByTestId('commit-timeout').selectOption('60')
  await page.getByTestId('apply-confirm').click()
  await expect(page.getByTestId('commit-banner')).toBeVisible()
  await page.getByTestId('confirm-commit').click()
  await expect(page.getByTestId('commit-banner')).toBeHidden()

  // running != startup -> save
  await expect(page.getByTestId('unsaved-bar')).toBeVisible()
  await page.getByTestId('save-config').click()
  await expect(page.getByTestId('unsaved-bar')).toBeHidden()
  const st = await (await page.request.get('/api/v1/commit')).json()
  expect(st.startup_differs).toBe(false)
  ports = await (await page.request.get('/api/v1/ports')).json()
  expect(ports.find((p) => p.name === 'vtnet2').kernel_pvid).toBe(20)
  expect(ports.find((p) => p.name === 'vtnet4').kernel_tagged).toEqual([20])
})

test('port editor validates input and stages a change', async ({ page }) => {
  await login(page, NEW_PW)
  await page.goto('/ports')
  await page.getByTestId('edit-vtnet3').click()
  await page.getByTestId('port-mode').selectOption('trunk')
  await page.getByTestId('port-tagged').fill('20,5000')
  await expect(page.getByText('VLAN out of range')).toBeVisible()
  await expect(page.getByTestId('port-save')).toBeDisabled()
  await page.getByTestId('port-tagged').fill('20')
  await page.getByTestId('port-save').click()
  await expect(page.getByTestId('pending-bar')).toBeVisible()
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByTestId('pending-bar')).toBeHidden()
})

test('mobile layout and dark mode', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await login(page, NEW_PW)
  await expect(page.locator('.chassis')).toBeVisible()
  await page.getByRole('button', { name: 'Menu' }).click()
  await page.getByRole('navigation', { name: 'main' }).getByRole('link', { name: 'Ports', exact: true }).click()
  await expect(page.getByTestId('ports-table')).toBeVisible()
  await page.getByRole('button', { name: 'Toggle dark mode' }).click()
  const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(bg).not.toBe('rgb(244, 246, 249)')
})

test('phase 2: ACL generates pf rules, LAG can be created', async ({ page }) => {
  await login(page, NEW_PW)
  await page.goto('/security')
  await page.getByRole('button', { name: 'Add ACL' }).click()
  await page.getByRole('button', { name: 'Add rule' }).click()
  await page.getByRole('row').filter({ has: page.locator('td', { hasText: /^1$/ }) }).locator('select').nth(1).selectOption('tcp')
  await page.getByPlaceholder('443 or 1000:2000').fill('22')
  await page.getByRole('button', { name: 'Stage ACLs' }).click()
  await expect(page.getByTestId('pending-bar')).toBeVisible()
  await page.getByRole('button', { name: 'Generated rules' }).click()
  await expect(page.getByTestId('pf-preview')).toContainText('block drop in quick on')
  await expect(page.getByTestId('pf-preview')).toContainText('proto tcp from any to any port 22')
  page.once('dialog', (d) => d.accept())
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(page.getByTestId('pending-bar')).toBeHidden()

  await page.goto('/lag')
  await page.getByRole('button', { name: 'New LAG' }).click()
  await page.getByLabel(/igb0 /).check()
  await page.getByLabel(/igb1 /).check()
  await page.getByRole('button', { name: 'Stage' }).click()
  await expect(page.getByRole('heading', { name: 'lagg0' })).toBeVisible()
  await page.getByTestId('review').click()
  await expect(page.getByTestId('commands')).toContainText('ifconfig lagg0 laggport igb0')
})
