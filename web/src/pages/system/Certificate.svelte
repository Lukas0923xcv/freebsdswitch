<script>
  import { get, post } from '../../lib/api.js'
  import { isAdmin, toast, errorToast } from '../../lib/store.svelte.js'
  import { time } from '../../lib/format.js'
  let info = $state(null)
  let cert = $state('')
  let key = $state('')
  async function load() { info = await get('/tls') }
  $effect(() => { load().catch(errorToast) })
  async function file(e, set) { const f = e.currentTarget.files[0]; if (f) set(await f.text()) }
  async function upload() {
    try { info = await post('/tls', { cert, key }); cert = key = ''; toast('Certificate installed (active immediately)', 'ok') } catch (e) { errorToast(e) }
  }
  async function regen() {
    if (!confirm('Replace the certificate with a new self-signed one?')) return
    try { info = await post('/tls', { regenerate: true }); toast('New self-signed certificate active', 'ok') } catch (e) { errorToast(e) }
  }
</script>

<div class="grid cols-2">
  <div class="card">
    <div class="card-head"><h2>Current certificate</h2>{#if info?.self_signed}<span class="badge warn">self-signed</span>{/if}</div>
    <div class="card-body">
      {#if info}
        <dl class="kv">
          <dt>Subject</dt><dd>{info.subject}</dd><dt>Issuer</dt><dd>{info.issuer}</dd>
          <dt>Valid</dt><dd>{time(info.not_before)} – {time(info.not_after)}</dd>
          <dt>Names</dt><dd>{[...(info.dns_names || []), ...(info.ips || [])].join(', ')}</dd>
          <dt>SHA-256</dt><dd class="mono small" style="word-break:break-all">{info.sha256_fingerprint}</dd>
        </dl>
        {#if isAdmin()}<button style="margin-top:12px" onclick={regen}>Generate new self-signed certificate</button>{/if}
      {/if}
    </div>
  </div>
  {#if isAdmin()}
    <div class="card">
      <div class="card-head"><h2>Upload certificate</h2></div>
      <div class="card-body stack">
        <div class="field"><label for="cf">Certificate (PEM, chain allowed)</label><input id="cf" type="file" accept=".pem,.crt,.cer" onchange={(e) => file(e, (v) => (cert = v))} /><textarea rows="5" bind:value={cert} placeholder="-----BEGIN CERTIFICATE-----"></textarea></div>
        <div class="field"><label for="kf">Private key (PEM, unencrypted)</label><input id="kf" type="file" accept=".pem,.key" onchange={(e) => file(e, (v) => (key = v))} /><textarea rows="5" bind:value={key} placeholder="-----BEGIN PRIVATE KEY-----"></textarea></div>
        <button class="primary" disabled={!cert || !key} onclick={upload}>Install</button>
      </div>
    </div>
  {/if}
</div>
