<script>
  import { api } from './api.js';
  let { onerror, onunauthorized } = $props();
  let users = $state([]), loading = $state(true), busy = $state(false);
  let username = $state(''), password = $state(''), target = $state(null), reset = $state(''), notice = $state(''), actionError = $state('');
  let dialog;
  function report(e) { if (e.status === 401) onunauthorized(); else onerror(e.message); }
  async function load() { try { users = await api('/admin/users'); } catch (e) { report(e); } finally { loading = false; } }
  $effect(() => { load(); });
  async function create(event) {
    event.preventDefault(); busy = true; notice = '';
    try { await api('/admin/users', 'POST', { username, password }); notice = `用户 ${username} 已创建`; username = ''; password = ''; await load(); }
    catch (e) { report(e); } finally { busy = false; }
  }
  async function disable(user) {
    busy = true;
    try { await api(`/admin/users/${user.id}`, 'PATCH', { disabled: !user.disabled }); await load(); }
    catch (e) { report(e); } finally { busy = false; }
  }
  async function resetPassword(event) {
    event.preventDefault(); busy = true;
    try { await api(`/admin/users/${target.id}/password`, 'POST', { password: reset }); notice = `${target.username} 的密码已重置，已有登录已退出`; reset = ''; dialog.close(); target = null; }
    catch (e) { actionError = e.message; report(e); } finally { busy = false; }
  }
</script>

<section aria-labelledby="users-title">
  <div class="section-heading"><div><p class="eyebrow">ADMINISTRATION</p><h1 id="users-title">用户管理</h1><p class="muted">由管理员创建账号，每个用户拥有独立的部署凭据。</p></div></div>
  <form class="panel create-bar" onsubmit={create}>
    <div class="field grow"><label for="new-username">用户名</label><input id="new-username" class="input" bind:value={username} required minlength="3" maxlength="32" pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{2,31}" autocomplete="off" /><p class="help">3–32 位字母、数字、下划线或连字符。</p></div>
    <div class="field grow"><label for="new-user-password">初始密码</label><input id="new-user-password" type="password" class="input" bind:value={password} required minlength="12" maxlength="72" autocomplete="new-password" /><p class="help">12–72 字节，中文字符通常占 3 字节。</p></div>
    <button class="btn btn-primary" disabled={busy}>{busy ? '处理中…' : '创建用户'}</button>
  </form>
  <p role="status" class="help">{notice}</p>
  {#if loading}<div class="panel empty" role="status" aria-busy="true">正在读取用户…</div>
  {:else}<div class="token-list">{#each users as user (user.id)}<article class="panel token-row"><div><div class="row"><h2>{user.username}</h2><span class="badge badge-outline">{user.admin ? '管理员' : user.disabled ? '已禁用' : '正常'}</span></div><p class="help">{user.admin ? '管理员账号' : user.disabled ? '登录及上传已停用，部署内容保留' : '可登录并使用部署 API'}</p></div>{#if !user.admin}<div class="actions"><button class="btn btn-ghost btn-sm" disabled={busy} onclick={() => { target = user; reset = ''; dialog.showModal(); }}>重置密码</button><button class="btn btn-ghost btn-sm" class:danger={!user.disabled} disabled={busy} onclick={() => disable(user)}>{user.disabled ? '启用' : '禁用'}</button></div>{/if}</article>{/each}</div>{/if}
</section>
<dialog class="modal" bind:this={dialog} onclose={() => { actionError = ''; }}><form class="modal-box" onsubmit={resetPassword}><h2>重置 {target?.username} 的密码</h2><p class="dialog-copy">此用户的所有登录会话将立即退出。</p>{#if actionError}<p class="danger" role="alert">{actionError}</p>{/if}<div class="field"><label for="reset-password">新密码</label><input id="reset-password" type="password" class="input" bind:value={reset} required minlength="12" maxlength="72" autocomplete="new-password" /></div><div class="modal-action"><button type="button" class="btn btn-ghost" disabled={busy} onclick={() => { reset = ''; dialog.close(); }}>取消</button><button class="btn btn-primary" disabled={busy}>{busy ? '处理中…' : '重置密码'}</button></div></form></dialog>
