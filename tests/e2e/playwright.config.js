// End-to-end tests for the web UI. The daemon runs in simulation mode
// (built-in kernel simulator), so this suite runs on any OS. To run the
// same tests against a real switch set BSDSWITCH_E2E_URL and
// BSDSWITCH_E2E_PASSWORD (initial admin password).
const { defineConfig } = require('@playwright/test')
const path = require('path')

const state = path.join(__dirname, '.state')
const port = process.env.BSDSWITCH_E2E_PORT || '18090'
const external = !!process.env.BSDSWITCH_E2E_URL

module.exports = defineConfig({
  testDir: '.',
  timeout: 90_000,
  workers: 1,
  fullyParallel: false,
  reporter: [['list']],
  use: {
    baseURL: process.env.BSDSWITCH_E2E_URL || `http://127.0.0.1:${port}`,
    ignoreHTTPSErrors: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  webServer: external ? undefined : {
    command: `rm -rf ${state} && mkdir -p ${state} && ${process.env.BSDSWITCH_BIN || '../../build/bsdswitch'} daemon -sim -config-dir ${state}/etc -state ${state}/db -socket ${state}/api.sock -listen-http 127.0.0.1:${port}`,
    url: `http://127.0.0.1:${port}/api/v1/version`,
    reuseExistingServer: false,
    timeout: 30_000,
  },
  metadata: { state },
})
