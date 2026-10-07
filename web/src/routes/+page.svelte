<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/api.js';
  import TokenPanel from '$lib/TokenPanel.svelte';
  import UserPanel from '$lib/UserPanel.svelte';
  let user = $state(null), loading = $state(true), busy = $state(false), error = $state(''), tab = $state('tokens');
  let username = $state(''), password = $state(''), current = $state(''), next = $state(''), theme = $state('xianii');
  let passwordDialog;
  onMount(async () => {
    theme = localStorage.getItem('easydist-theme') === 'xianii-light' ? 'xianii-light' : 'xianii';
    document.documentElement.dataset.theme = theme;
    try { user = await api('/me'); } catch (e) { if (e.status !== 401) error = e.message; } finally { loading = false; }
  });
  function toggleTheme() { theme = theme === 'xianii' ? 'xianii-light' : 'xianii'; document.documentElement.dataset.theme = theme; localStorage.setItem('easydist-theme', theme); }
  function unauthorized() { user = null; tab = 'tokens'; error = '登录已失效，请重新登录。'; }
  async function login(event) {
    event.preventDefault(); busy = true; error = '';
    try { user = await api('/login', 'POST', { username, password }); password = ''; }
    catch (e) { error = e.message; } finally { busy = false; }
  }
  async function logout() {
    busy = true; error = '';
    try { await api('/logout', 'POST'); user = null; tab = 'tokens'; }
    catch (e) { if (e.status === 401) user = null; else error = e.message; } finally { busy = false; }
  }
  async function changePassword(event) {
    event.preventDefault(); busy = true; error = '';
    try { await api('/password', 'POST', { current_password: current, password: next }); passwordDialog.close(); current = ''; next = ''; user = null; tab = 'tokens'; }
    catch (e) { error = e.message; if (e.status === 401) { passwordDialog.close(); unauthorized(); } } finally { busy = false; }
  }
</script>

<svelte:head><title>EasyDist · Godot Web 部署</title><meta name="description" content="EasyDist 部署凭据与用户管理" /></svelte:head>
<a class="skip-link" href="#main">跳到主要内容</a>
<div class="shell">
  <header><a class="brand" href="/"><span class="brand-icon" aria-hidden="true">e<span>↗</span></span><span>EasyDist<small>GODOT WEB HOSTING</small></span></a><div class="header-actions">{#if user}<span class="username">{user.username}</span><button class="btn btn-ghost btn-sm" onclick={() => { current = ''; next = ''; passwordDialog.showModal(); }}>修改密码</button><button class="btn btn-ghost btn-sm" disabled={busy} onclick={logout}>退出</button>{/if}<button class="btn btn-square btn-ghost" onclick={toggleTheme} aria-label={theme === 'xianii' ? '切换浅色主题' : '切换深色主题'}>{theme === 'xianii' ? '☀' : '☾'}</button></div></header>
  {#if user}<nav aria-label="管理导航"><button class:active={tab === 'tokens'} aria-current={tab === 'tokens' ? 'page' : undefined} onclick={() => { tab = 'tokens'; error = ''; }}>部署凭据</button>{#if user.admin}<button class:active={tab === 'users'} aria-current={tab === 'users' ? 'page' : undefined} onclick={() => { tab = 'users'; error = ''; }}>用户管理</button>{/if}</nav>{/if}
  <main id="main">
    {#if error}<div class="alert alert-error error-banner" role="alert"><span>{error}</span><button class="btn btn-ghost btn-sm" aria-label="关闭错误提示" onclick={() => error = ''}>关闭</button></div>{/if}
    {#if loading}<div class="empty" role="status" aria-busy="true">正在连接 EasyDist…</div>
    {:else if !user}<div class="login-wrap"><div class="login-intro"><p class="eyebrow">FROM EXPORT TO PLAY</p><h1>让游戏<br />有一个地址。</h1><p class="muted">导出 Godot Web 包，上传 ZIP。<br />EasyDist 把剩下的步骤缩成一次请求。</p><div class="login-detail"><span class="mono">01</span><span>创建部署 token</span></div><div class="login-detail"><span class="mono">02</span><span>通过 API 上传</span></div><div class="login-detail"><span class="mono">03</span><span>打开链接，开始游戏</span></div></div><form class="panel login-form" onsubmit={login}><h2>登录管理台</h2><p class="muted">使用管理员提供的账号。</p><div class="field"><label for="username">用户名</label><input class="input" id="username" bind:value={username} required autocomplete="username" maxlength="32" /></div><div class="field"><label for="password">密码</label><input class="input" id="password" type="password" bind:value={password} required autocomplete="current-password" maxlength="72" /></div><button class="btn btn-primary" disabled={busy}>{busy ? '登录中…' : '登录 →'}</button><p class="help">不提供注册。需要账号时请联系管理员。</p></form></div>
    {:else if tab === 'tokens'}<TokenPanel onerror={(message) => error = message} onunauthorized={unauthorized} />
    {:else}<UserPanel onerror={(message) => error = message} onunauthorized={unauthorized} />{/if}
  </main>
  <footer><span>EasyDist</span><span>小一点的系统，少一点的步骤。</span></footer>
</div>
<dialog class="modal" bind:this={passwordDialog}><form class="modal-box" onsubmit={changePassword}><h2>修改密码</h2><p class="dialog-copy">修改后所有登录会话将退出，请使用新密码重新登录。</p>{#if error}<p class="danger" role="alert">{error}</p>{/if}<div class="field"><label for="current-password">当前密码</label><input class="input" id="current-password" type="password" bind:value={current} required autocomplete="current-password" /></div><div class="field"><label for="next-password">新密码</label><input class="input" id="next-password" type="password" bind:value={next} required minlength="12" maxlength="72" autocomplete="new-password" /><p class="help">12–72 字节。</p></div><div class="modal-action"><button type="button" class="btn btn-ghost" disabled={busy} onclick={() => { current = ''; next = ''; passwordDialog.close(); }}>取消</button><button class="btn btn-primary" disabled={busy}>{busy ? '处理中…' : '更新密码'}</button></div></form></dialog>
