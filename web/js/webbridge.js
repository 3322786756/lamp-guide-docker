/* ==========================================================================
   webbridge.js —— 浏览器版的宿主通道（容器部署用）。

   桌面版由 WebView2 注入 window.chrome.webview：页面 postMessage，宿主回调
   window.__hostReceive。容器里没有这个宿主，所以这里补一个同样形状的对象，把
   每条消息转成 POST /admin/rpc，再把回包原样交给 __hostReceive —— bridge.js
   和所有页面一行都不用改。

   只有桌面才做得成的动作，在浏览器里有更自然的做法，就地在这里处理：
     OpenUrl      → 新标签页打开
     PickOpenPath → <input type="file"> 选文件，上传换成服务端路径，import 照旧
     PickSavePath → 不问保存位置，回建议文件名，导出改走浏览器下载
   ========================================================================== */

(function () {
  // 桌面版已经注入了真宿主：一个字节都别抢。
  if (window.chrome && window.chrome.webview) return;

  const RPC = '/admin/rpc';
  const UPLOAD = '/admin/upload';

  function reply(id, ok, data, error) {
    if (typeof window.__hostReceive !== 'function') return;
    const msg = { id: id, ok: !!ok };
    if (data !== undefined) msg.data = data;
    if (error) msg.error = String(error);
    window.__hostReceive(JSON.stringify(msg));
  }

  async function rpc(message) {
    const res = await fetch(RPC, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(message)
    });
    if (res.status === 401 || res.status === 403) {
      // 会话没了（主密钥换过、cookie 被清）：回登录页重新来过，而不是让页面
      // 一个接一个地弹「宿主通道不可用」。
      location.replace('/admin');
      throw new Error('需要重新登录');
    }
    return await res.json();
  }

  /* 选文件：浏览器没有原生对话框给页面，用一个隐藏的 <input type="file">。 */
  function pickFile() {
    return new Promise((resolve) => {
      const input = document.createElement('input');
      input.type = 'file';
      input.accept = '.json,application/json';
      input.style.position = 'fixed';
      input.style.left = '-1000px';
      input.addEventListener('change', () => {
        const file = input.files && input.files[0];
        input.remove();
        resolve(file || null);
      });
      input.addEventListener('cancel', () => {
        input.remove();
        resolve(null);
      });
      document.body.appendChild(input);
      input.click();
    });
  }

  /* 上传：网关读得到的是服务端路径，所以文件先落地再交给 import。 */
  async function upload(file) {
    const res = await fetch(UPLOAD + '?name=' + encodeURIComponent(file.name || 'config.json'), {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'content-type': 'application/octet-stream' },
      body: file
    });
    if (res.status === 401 || res.status === 403) {
      location.replace('/admin');
      throw new Error('需要重新登录');
    }
    return await res.json();
  }

  /* 导出：网关把字节存成一次性令牌，这里点一下链接让浏览器自己下载。 */
  function download(token) {
    const a = document.createElement('a');
    a.href = '/admin/download/' + encodeURIComponent(String(token));
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  window.chrome = window.chrome || {};
  window.chrome.webview = {
    postMessage: function (raw) {
      let req;
      try {
        req = JSON.parse(raw);
      } catch (err) {
        return;
      }
      const id = req && req.id;
      const method = String((req && req.method) || '');
      const payload = (req && req.payload) || {};

      if (method === 'OpenUrl' || method === 'OpenURL') {
        const url = String(payload.url || '');
        if (url) window.open(url, '_blank', 'noopener');
        reply(id, true, { url: url });
        return;
      }

      if (method === 'PickSavePath') {
        const name = String(payload.name || 'lamp-guide-export.bin');
        reply(id, true, { path: name, canceled: false });
        return;
      }

      if (method === 'PickOpenPath') {
        pickFile()
          .then(async (file) => {
            if (!file) {
              reply(id, true, { canceled: true });
              return;
            }
            const up = await upload(file);
            if (!up || !up.ok) {
              reply(id, false, undefined, (up && up.error) || '上传失败');
              return;
            }
            reply(id, true, { path: String((up.data || {}).path || ''), canceled: false });
          })
          .catch((err) => reply(id, false, undefined, (err && err.message) || err));
        return;
      }

      rpc({ id: id, method: method, payload: payload })
        .then((res) => {
          const data = res && res.data;
          if (data && typeof data === 'object' && data.download) download(data.download);
          reply(id, !!(res && res.ok), data, (res && res.error) || '');
        })
        .catch((err) => reply(id, false, undefined, (err && err.message) || err));
    }
  };
})();
