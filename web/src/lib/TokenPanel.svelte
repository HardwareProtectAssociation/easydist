<script>
  import { api } from './api.js';
  let { onerror, onunauthorized } = $props();
  let tokens = $state([]), loading = $state(true), busy = $state(false);
  let name = $state(''), issued = $state(null), copied = $state('');
  let dialog;
  let deletion = $state(null), rotation = $state(null), actionError = $state('');

  async function load() {
    loading = true;
    try { tokens = await api('/tokens'); }
    catch (e) { report(e); }
    finally { loading = false; }
  }
  function report(e) { if (e.status === 401) onunauthorized(); else onerror(e.message); }
  $effect(() => { load(); });
  async function create(event) {
    event.preventDefault(); busy = true;
    try { issued = await api('/tokens', 'POST', { dist_name: name }); name = ''; copied = ''; await load(); }
    catch (e) { report(e); }
    finally { busy = false; }
  }
  async function confirmAction() {
    busy = true;
    try {
      if (deletion) { await api(`/tokens/${deletion.id}`, 'DELETE'); if (issued?.id === deletion.id) issued = null; }
      else { issued = await api(`/tokens/${rotation.id}/rotate`, 'POST'); copied = ''; }
      dialog.close(); deletion = null; rotation = null; await load();
    } catch (e) { actionError = e.message; report(e); }
    finally { busy = false; }
  }
  async function copy(value, label) {
    try { await navigator.clipboard.writeText(value); copied = `${label}已复制`; }
    catch { onerror('无法访问剪贴板，请手动选择并复制。'); }
  }
  function command(token) {
    return `curl --fail-with-body -X POST '${location.origin}/api/deploy' \\\n  -H 'Authorization: Bearer ${token}' \\\n  -H 'Content-Type: application/zip' \\\n  --data-binary @game.zip`;
  }
</script>

<section aria-labelledby="token-title">
  <div class="section-heading"><div><p class="eyebrow">DEPLOYMENT TOKENS</p><h1 id="token-title">部署凭据</h1><p class="muted">一个 token 对应一个固定地址。上传 ZIP，即完成部署。</p></div><div class="row"><span class="badge badge-outline quota" aria-label="token 使用额度">{tokens.length} / 16 已用</span><button class="btn btn-ghost btn-sm" disabled={busy || loading} onclick={load}>刷新</button></div></div>

  <form class="panel create-bar" onsubmit={create}>
    <div class="field grow"><label for="dist-name">新建 dist name</label><input id="dist-name" class="input" placeholder="my-game" bind:value={name} required minlength="3" maxlength="48" pattern="[a-z0-9][a-z0-9-]{1,46}[a-z0-9]" aria-describedby="name-help" disabled={busy || loading || tokens.length >= 16} /><p class="help" id="name-help">3–48 位小写字母、数字、连字符；首尾为字母或数字，全局唯一。</p></div>
    <button class="btn btn-primary" disabled={busy || loading || tokens.length >= 16}>{busy ? '处理中…' : '创建 token'}</button>
  </form>
  {#if tokens.length >= 16}<p class="notice">已达到 16 个 token 的上限。删除一个部署后可继续创建。</p>{/if}

  {#if issued}
    <section class="panel secret-panel" aria-labelledby="secret-title">
      <div class="row"><h2 id="secret-title">保存你的 token</h2><button class="btn btn-ghost btn-sm" onclick={() => issued = null}>关闭</button></div>
      <p class="muted">这是 {issued.dist_name} 的凭据，仅显示这一次。关闭后无法重新查看。</p>
      <div class="copy-line"><input class="input mono grow" aria-label="新生成的 token" readonly value={issued.token} /><button class="btn btn-outline" onclick={() => copy(issued.token, 'Token')}>复制 token</button></div>
      <h3>使用 API 上传</h3><textarea class="command" aria-label="上传命令" readonly rows="5" value={command(issued.token)}></textarea><button class="btn btn-ghost btn-sm" onclick={() => copy(command(issued.token), '上传命令')}>复制上传命令</button><p role="status" class="help">{copied}</p>
    </section>
  {/if}

  {#if loading}<div class="panel empty" role="status" aria-busy="true">正在读取部署凭据…</div>
  {:else if tokens.length === 0}<div class="panel empty"><span class="empty-mark" aria-hidden="true">↗</span><h2>从第一个游戏开始</h2><p class="muted">创建一个名称，保存 token，然后上传 Godot Web 导出包。</p></div>
  {:else}
    <div class="token-list" aria-label="部署列表">
      {#each tokens as token (token.id)}
        <article class="panel token-row">
          <div class="token-info"><div class="row"><h2 class="mono">{token.dist_name}</h2><span class:badge-success={token.deployed && !token.deleting} class="badge badge-soft">{token.deleting ? '删除待完成' : token.deployed ? '已部署' : '待上传'}</span></div><a class="deploy-url" href={token.url} target="_blank" rel="noopener noreferrer">{token.url} <span aria-hidden="true">↗</span></a><p class="help">{token.deployed_at ? `最近部署 ${new Date(token.deployed_at * 1000).toLocaleString('zh-CN')}` : `创建于 ${new Date(token.created_at * 1000).toLocaleDateString('zh-CN')}`}</p></div>
          <div class="actions"><button class="btn btn-ghost btn-sm" disabled={busy || token.deleting} onclick={() => { rotation = token; deletion = null; actionError = ''; dialog.showModal(); }}>轮换 token</button><button class="btn btn-ghost btn-sm danger" disabled={busy} onclick={() => { deletion = token; rotation = null; actionError = ''; dialog.showModal(); }}>删除</button></div>
        </article>
      {/each}
    </div>
  {/if}
  <p class="footnote">ZIP 根目录或唯一顶层目录须包含 index.html。再次上传会完全覆盖旧内容。</p>
</section>

<dialog class="modal" bind:this={dialog} oncancel={() => { deletion = null; rotation = null; }}>
  <div class="modal-box"><h2>{deletion ? '删除部署' : '轮换 token'}</h2><p class="dialog-copy">{deletion ? `将撤销 ${deletion.dist_name} 的 token、删除全部部署文件，并释放名称。此操作无法撤销。` : `为 ${rotation?.dist_name || ''} 生成新的 token。旧凭据立即失效，已部署游戏和访问地址保持不变。`}</p>{#if actionError}<p class="danger" role="alert">{actionError}</p>{/if}<div class="modal-action"><button class="btn btn-ghost" disabled={busy} onclick={() => { dialog.close(); deletion = null; rotation = null; }}>取消</button><button class="btn" class:btn-error={!!deletion} class:btn-primary={!deletion} disabled={busy} onclick={confirmAction}>{busy ? '处理中…' : deletion ? '删除全部内容' : '生成新 token'}</button></div></div>
</dialog>
