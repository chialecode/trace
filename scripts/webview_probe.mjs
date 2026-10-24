// Drive only the isolated TRACE WebView supplied by the test launcher. Node's
// built-in WebSocket implements CDP; no desktop input, foregrounding or browser
// profile discovery. The packaged product contains no debugging endpoint.
import { writeFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';

const [portText, action = 'inspect', screenshot] = process.argv.slice(2);
const port = Number(portText);
if (!Number.isInteger(port) || port < 1024 || port > 65535 || !['inspect','layout-matrix','status-entry','close','exit','disconnected'].includes(action)) throw Error('invalid probe arguments');
const base = `http://127.0.0.1:${port}`;
let target;
for (let i = 0; i < 150; i++) {
  try {
    const response = await fetch(`${base}/json/list`, {signal:AbortSignal.timeout(1000)});
    const pages = await response.json();
    target = pages.find(p => p.type === 'page' && /^(https?:\/\/tauri\.localhost|tauri:\/\/localhost)(\/|$)/.test(p.url));
    if (target) break;
  } catch { /* Starting WebView: retry within the fixed 30 second budget. */ }
  await delay(200);
}
if (!target) throw Error('embedded production page missing');
const debug = new URL(target.webSocketDebuggerUrl);
if (!['127.0.0.1','localhost'].includes(debug.hostname) || Number(debug.port) !== port || debug.protocol !== 'ws:') throw Error('unexpected debugger target');
const socket = new WebSocket(debug);
await new Promise((resolve,reject)=>{socket.addEventListener('open',resolve,{once:true});socket.addEventListener('error',reject,{once:true});});
let next = 1;
const pending = new Map();
socket.addEventListener('message',event=>{
  const msg=JSON.parse(event.data);const request=pending.get(msg.id);
  if (request) { pending.delete(msg.id);clearTimeout(request.timer);if(msg.error)request.reject(Error(JSON.stringify(msg.error)));else request.resolve(msg.result); }
});
function command(method,params={}) {
  return new Promise((resolve,reject)=>{const id=next++;const timer=setTimeout(()=>{pending.delete(id);reject(Error(`CDP timeout: ${method}`));},15000);pending.set(id,{resolve,reject,timer});socket.send(JSON.stringify({id,method,params}));});
}
async function evaluate(expression) {
  const result=await command('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});
  if(result.exceptionDetails)throw Error('WebView evaluation failed: '+JSON.stringify(result.exceptionDetails));
  return result.result.value;
}
async function waitFor(expression, failure, attempts = 100) {
  for (let i = 0; i < attempts; i++) {
    if (await evaluate(expression)) return;
    await delay(100);
  }
  throw Error(failure);
}
try {
  for(let i=0;i<100;i++) {
    if(await evaluate("Boolean(window.__TAURI_INTERNALS__ && document.querySelector('#app')?.children.length)"))break;
    if(i===99)throw Error('application did not mount');
    await delay(100);
  }
  if(action==='disconnected') {
    let disconnected=false;
    for(let i=0;i<80;i++) {disconnected=await evaluate("document.body.innerText.includes('无法连接独立后台')");if(disconnected)break;await delay(100);}
    if(!disconnected)throw Error('other desktop did not show disconnection');
    console.log(JSON.stringify({disconnected:true}));
  } else if(action==='layout-matrix') {
    const windowState=await evaluate("window.__TAURI_INTERNALS__.invoke('desktop_window_state')");
    const dragRegions=await evaluate("document.querySelectorAll('.window-titlebar [data-tauri-drag-region]').length");
    const windowButtons=await evaluate("document.querySelectorAll('.window-titlebar .window-controls button').length");
    if(dragRegions<2||windowButtons!==3||typeof windowState?.maximized!=='boolean')throw Error('immersive titlebar controls are incomplete');
    const toggled=await evaluate("window.__TAURI_INTERNALS__.invoke('desktop_window_toggle_maximize')");
    const restored=await evaluate("window.__TAURI_INTERNALS__.invoke('desktop_window_toggle_maximize')");
    if(toggled?.maximized!==!windowState.maximized||restored?.maximized!==windowState.maximized)throw Error('native maximize/restore did not change state');
    // The drag-region runtime uses plugin commands, not the custom button commands.
    await evaluate("document.querySelector('.window-drag-space').dispatchEvent(new MouseEvent('mousedown',{button:0,detail:2,bubbles:true}))");
    await waitFor(`window.__TAURI_INTERNALS__.invoke('desktop_window_state').then(s=>s.maximized===${!windowState.maximized})`,'drag-region double click was not permitted');
    await evaluate("document.querySelector('.window-drag-space').dispatchEvent(new MouseEvent('mousedown',{button:0,detail:2,bubbles:true}))");
    await waitFor(`window.__TAURI_INTERNALS__.invoke('desktop_window_state').then(s=>s.maximized===${windowState.maximized})`,'drag-region double click did not restore');
    await evaluate("window.__TAURI_INTERNALS__.invoke('plugin:window|start_dragging')");
    await evaluate(`(async()=>{const id=await window.__TAURI_INTERNALS__.invoke('plugin:event|listen',{event:'trace://change',target:{kind:'Any'},handler:window.__TAURI_INTERNALS__.transformCallback(()=>{})});await window.__TAURI_INTERNALS__.invoke('plugin:event|unlisten',{event:'trace://change',eventId:id});})()`);
    await command('Emulation.setDeviceMetricsOverride',{width:720,height:900,deviceScaleFactor:1,mobile:false});
    await delay(150);
    // The component test covers rejected IPC; exercise its scoped error CSS in WebView.
    await evaluate(`(()=>{const host=document.querySelector('.window-titlebar'),n=document.createElement('span');for(const a of host.attributes)if(a.name.startsWith('data-v-'))n.setAttribute(a.name,'');n.className='window-error';n.dataset.probeError='true';n.textContent='Synthetic window action failure';host.querySelector('.window-controls').before(n)})()`);
    try {
      await waitFor("Boolean(document.querySelector('.window-error')?.textContent)",'window action error missing');
      const visibleError=await evaluate("(()=>{const n=document.querySelector('.window-error'),r=n.getBoundingClientRect();return getComputedStyle(n).display!=='none'&&r.width>0&&r.height>0})()");
      if(!visibleError)throw Error('window action error hidden at the minimum desktop width');
    } finally {
      await evaluate("document.querySelector('[data-probe-error]')?.remove()");
    }
    const routes=[['/settings','settings'],['/','home'],['/creators','creators'],['/timeline','timeline'],['/explorer','explorer'],['/tasks','tasks'],['/management','management']];
    const scales=[100,110,125,150];
    const widths=[720,980,1280,1536];
    const observations=[];
    const viewport=async width=>{
      await command('Emulation.setDeviceMetricsOverride',{width,height:900,deviceScaleFactor:1,mobile:false,screenWidth:width,screenHeight:900});
    };
    const navigate=async route=>{
      const expected='#'+route;
      for(let attempt=0;attempt<3;attempt++){
        await evaluate(`location.hash=${JSON.stringify(expected)}`);
        await delay(300);
        if(await evaluate('location.hash')===expected)return;
      }
      throw Error(`same-document navigation was ignored: ${route}`);
    };
    const chooseScale=async scale=>{
      await navigate('/settings');
      await waitFor("Boolean(document.querySelector('[data-page=\"settings\"]'))",'settings page missing');
      await evaluate(`document.querySelector('[data-text-scale-option="${scale}"]').click()`);
      await waitFor(`document.documentElement.dataset.textScale==='${scale}'`,`text scale ${scale} did not apply`);
    };
    const measure=async(route,page,width,scale)=>{
      const target=route==='/'?`/?layout=${width}-${scale}`:route;
      await navigate(target);
      try {
        await waitFor(`Boolean(document.querySelector('[data-page="${page}"]'))`,`route ${route} missing`);
      } catch {
        const state=await evaluate(`({hash:location.hash,text:document.querySelector('main')?.innerText?.slice(0,400),pages:Array.from(document.querySelectorAll('[data-page]')).map(n=>n.dataset.page)})`);
        throw Error(`route ${route} (${width}/${scale}) missing: ${JSON.stringify(state)}`);
      }
      const value=await evaluate(`(()=>{
        const main=document.querySelector('main');
        const root=getComputedStyle(document.documentElement);
        const page=document.querySelector('[data-page="${page}"]');
        const nav=document.querySelector('.main-nav a');
        const button=document.querySelector('main button, main .n-button');
        const mainBox=main.getBoundingClientRect();
        const overflowing=Array.from(document.querySelectorAll('[data-page] *'))
          .filter(node=>{const box=node.getBoundingClientRect();return box.right>mainBox.right+1||box.width>main.clientWidth+1})
          .slice(0,12)
          .map(node=>({tag:node.tagName,cls:String(node.className||'').slice(0,100),text:String(node.textContent||'').trim().slice(0,80),width:Math.round(node.getBoundingClientRect().width),right:Math.round(node.getBoundingClientRect().right)}));
        const shellOverflowing=Array.from(document.querySelectorAll('.topbar *, .window-titlebar *, .sidebar'))
          .filter(node=>node.getBoundingClientRect().right>innerWidth+1||node.getBoundingClientRect().left<-1)
          .slice(0,12)
          .map(node=>({tag:node.tagName,cls:String(node.className||'').slice(0,100),text:String(node.textContent||'').trim().slice(0,80),width:Math.round(node.getBoundingClientRect().width),right:Math.round(node.getBoundingClientRect().right)}));
        const before={scrollHeight:main.scrollHeight,clientHeight:main.clientHeight,scrollTop:main.scrollTop};
        main.scrollTop=main.scrollHeight;
        const reached=main.scrollHeight<=main.clientHeight+1||main.scrollTop+main.clientHeight>=main.scrollHeight-2;
        const after=main.scrollTop;
        main.scrollTop=before.scrollTop;
        return {
          rootFont:parseFloat(root.fontSize),navFont:nav?parseFloat(getComputedStyle(nav).fontSize):null,
          buttonHeight:button?Math.round(button.getBoundingClientRect().height*100)/100:null,
          viewport:{width:innerWidth,height:innerHeight},
          bodyOverflow:document.documentElement.scrollWidth-innerWidth,
          mainOverflow:main.scrollWidth-main.offsetWidth,
          overflowing,
          shellOverflowing,
          pageWidth:page?Math.round(page.getBoundingClientRect().width*100)/100:null,
          reachedBottom:reached,scrollTop:after
        };
      })()`);
      const expected=16*scale/100;
      if(Math.abs(value.rootFont-expected)>0.01)throw Error(`root size ${value.rootFont} != ${expected}`);
      if(value.viewport.width!==width)throw Error(`viewport ${value.viewport.width} != ${width}`);
      if(value.bodyOverflow>1||value.mainOverflow>1)throw Error(`horizontal overflow on ${route} at ${width}/${scale}: ${JSON.stringify(value)}`);
      if(!value.reachedBottom)throw Error(`bottom is unreachable on ${route} at ${width}/${scale}`);
      observations.push({route,page,width,scale,...value});
    };
    for(const scale of scales){
      await chooseScale(scale);
      for(const width of widths){
        await viewport(width);
        for(const [route,page] of routes)await measure(route,page,width,scale);
      }
    }
    await viewport(360);
    await chooseScale(150);
    await evaluate("document.querySelector('.window-titlebar').style.display='none'");
    for (const [route, page] of routes) await measure(route, page, 360, 150);
    await evaluate("document.querySelector('.window-titlebar').style.removeProperty('display')");
    await viewport(360);
    await chooseScale(150);
    await navigate('/creators');
    await waitFor("Boolean(document.querySelector('[data-page=\"creators\"]'))",'creators page missing');
    await evaluate("Array.from(document.querySelectorAll('button')).find(b=>b.innerText.includes('新建创作者')).click()");
    await waitFor("Boolean(document.querySelector('.n-modal'))",'creator modal missing');
    const modal=await evaluate(`(()=>{const node=document.querySelector('.n-modal');const m=node.getBoundingClientRect();return {left:m.left,right:m.right,top:m.top,bottom:m.bottom,width:innerWidth,height:innerHeight,fontSize:parseFloat(getComputedStyle(node).fontSize),bodyScrollable:node.scrollHeight>=node.clientHeight}})()`);
    if(modal.left < -1 || modal.right > modal.width+1 || modal.top < -1 || modal.bottom > modal.height+1)throw Error('creator modal exceeds the viewport');
    if(modal.fontSize<=18)throw Error('creator modal did not scale with the selected text size');
    await evaluate("document.querySelector('.n-modal .n-base-close')?.click()");
    await waitFor("!document.querySelector('.n-modal')",'creator modal did not close');
    await navigate('/tasks');
    await waitFor("Boolean(document.querySelector('[data-page=\"tasks\"] .n-base-selection'))",'task filters missing');
    await evaluate("document.querySelector('[data-page=\"tasks\"] .n-base-selection').click()");
    await waitFor("Boolean(document.querySelector('.n-base-select-menu'))",'select floating layer missing');
    const floating=await evaluate(`(()=>{const node=document.querySelector('.n-base-select-menu');const m=node.getBoundingClientRect();return {left:m.left,right:m.right,top:m.top,bottom:m.bottom,width:innerWidth,height:innerHeight,fontSize:parseFloat(getComputedStyle(node).fontSize)}})()`);
    if(floating.left < -1 || floating.right > floating.width+1 || floating.top < -1 || floating.bottom > floating.height+1)throw Error('Naive UI floating layer exceeds the viewport');
    if(floating.fontSize<=18)throw Error(`Naive UI floating layer did not scale with the selected text size: ${JSON.stringify(floating)}`);
    await evaluate("document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))");
    await command('Emulation.clearDeviceMetricsOverride');
    await evaluate("window.__TAURI_INTERNALS__.invoke('desktop_window_minimize')");
    const at100=observations.find(x=>x.route==='/'&&x.width===1536&&x.scale===100);
    const at150=observations.find(x=>x.route==='/'&&x.width===1536&&x.scale===150);
    if(!at100||!at150||at150.navFont<=at100.navFont)throw Error('text scale did not change computed navigation size');
    console.log(JSON.stringify({matrix:observations.length,widths,scales,browser360:true,titlebarActions:true,modalWithinViewport:true,floatingLayerWithinViewport:true,computedFontChanged:true,noHorizontalOverflow:true,observations}));
  } else if(action==='inspect' || action==='status-entry') {
    if(action==='status-entry') {
      await waitFor("location.hash === '#/status' && Boolean(document.querySelector('main [data-action=\"desktop-exit\"]'))", 'notification status entry did not load');
    }
    const status=await evaluate("window.__TAURI_INTERNALS__.invoke('daemon_call',{command:'daemon.status',args:{}})");
    if(!status?.instance_id || !Number.isInteger(status.schema_version))throw Error('daemon status unavailable');
    const routes=[['/','main .home-layout'],['/acquire','main .source-acquisition'],['/resources','main .resource-library'],['/upgrades','main .upgrades'],['/status','main [data-action="desktop-exit"]']];
    for(const [route,selector] of routes) {
      await evaluate(`location.hash=${JSON.stringify('#'+route)}`);
      await waitFor(`Boolean(document.querySelector(${JSON.stringify(selector)}))`, `route missing: ${route}`);
    }
    const r2=await evaluate("Promise.all([window.__TAURI_INTERNALS__.invoke('daemon_call',{command:'upgrade.list',args:{state:'pending'}}),window.__TAURI_INTERNALS__.invoke('daemon_call',{command:'archive.timeline',args:{authentication:'unknown',limit:1}})])");
    if(!Array.isArray(r2?.[0]?.items) || typeof r2?.[0]?.total!=='number' || !r2?.[1]?.counts)throw Error('r2 queries did not reach Core');
    const direct=await evaluate("window.__TAURI_INTERNALS__.invoke('daemon_call',{command:'source.list',args:{}})");
    if(!Array.isArray(direct?.items))throw Error('r3 source query did not reach Core');
    await waitFor(`document.querySelector('main').innerText.includes(${JSON.stringify(status.instance_id)})`, 'status instance did not render');
    const body=await evaluate('document.body.innerText');
    if(!body.includes(status.instance_id) || body.includes('界面预览 · 示例数据') || !body.includes('关闭界面继续后台') || !body.includes('完全退出'))throw Error('status page is stale, demo or incomplete');
    if(screenshot){const shot=await command('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});await writeFile(screenshot,Buffer.from(shot.data,'base64'));}
    console.log(JSON.stringify({instanceId:status.instance_id,schemaVersion:status.schema_version,embeddedPage:true,acquisitionQueries:true,sourceQueries:true,routes:routes.map(r=>r[0]),lifecycleControls:true,notificationEntry:action==='status-entry',productAcceptance:'not-run'}));
  } else {
    await evaluate("location.hash='#/status'");
    const selector=`[data-action="desktop-${action}"]`;
    let present=false;
    for(let i=0;i<100;i++){present=await evaluate(`Boolean(document.querySelector(${JSON.stringify(selector)}))`);if(present)break;await delay(100);}
    if(!present)throw Error('lifecycle control missing');
    const result=await evaluate(`(()=>{const button=document.querySelector(${JSON.stringify(selector)});if(button.disabled)throw Error('lifecycle action disabled');const unix=Date.now()/1000;setTimeout(()=>button.click(),100);return {action:${JSON.stringify(action)},unix};})()`);
    console.log(JSON.stringify(result));
  }
} finally {
  // Wait for the native socket handle to close even on assertion failure.
  // Otherwise Node on Windows can exit during WebView/CDP teardown.
  if (socket.readyState !== WebSocket.CLOSED) {
    await new Promise(resolve => {
      const timer=setTimeout(resolve,2000);
      socket.addEventListener('close',()=>{clearTimeout(timer);resolve();},{once:true});
      socket.close();
    });
  }
}
