// Drive only the isolated TRACE WebView supplied by the test launcher. Node's
// built-in WebSocket implements CDP; no desktop input, foregrounding or browser
// profile discovery. The packaged product contains no debugging endpoint.
import { writeFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';

const [portText, action = 'inspect', screenshot] = process.argv.slice(2);
const port = Number(portText);
if (!Number.isInteger(port) || port < 1024 || port > 65535 || !['inspect','status-entry','close','exit','disconnected'].includes(action)) throw Error('invalid probe arguments');
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
  } else if(action==='inspect' || action==='status-entry') {
    if(action==='status-entry') {
      await waitFor("location.hash === '#/status' && Boolean(document.querySelector('main [data-action=\"desktop-exit\"]'))", 'notification status entry did not load');
    }
    const status=await evaluate("window.__TAURI_INTERNALS__.invoke('daemon_status')");
    if(!status?.instance_id || status.schema_version!==6)throw Error('daemon status unavailable');
    const routes=[['/','main .home-layout'],['/acquire','main .source-acquisition'],['/resources','main .resource-library'],['/upgrades','main .upgrades'],['/status','main [data-action="desktop-exit"]']];
    for(const [route,selector] of routes) {
      await evaluate(`location.hash=${JSON.stringify('#'+route)}`);
      await waitFor(`Boolean(document.querySelector(${JSON.stringify(selector)}))`, `route missing: ${route}`);
    }
    const r2=await evaluate("Promise.all([window.__TAURI_INTERNALS__.invoke('upgrade_list',{request:{state:'pending'}}),window.__TAURI_INTERNALS__.invoke('archive_timeline',{authentication:'unknown',limit:1})])");
    if(!Array.isArray(r2?.[0]?.items) || typeof r2?.[0]?.total!=='number' || !r2?.[1]?.counts)throw Error('r2 queries did not reach Core');
    const direct=await evaluate("window.__TAURI_INTERNALS__.invoke('source_list')");
    if(!Array.isArray(direct?.items))throw Error('r3 source query did not reach Core');
    await waitFor(`document.querySelector('main').innerText.includes(${JSON.stringify(status.instance_id)})`, 'status instance did not render');
    const body=await evaluate('document.body.innerText');
    if(!body.includes(status.instance_id) || body.includes('界面预览 · 示例数据') || !body.includes('关闭界面继续后台') || !body.includes('完全退出'))throw Error('status page is stale, demo or incomplete');
    if(screenshot){const shot=await command('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});await writeFile(screenshot,Buffer.from(shot.data,'base64'));}
    console.log(JSON.stringify({instanceId:status.instance_id,schemaVersion:status.schema_version,embeddedPage:true,r2AcquisitionQueries:true,r3SourceQueries:true,routes:routes.map(r=>r[0]),lifecycleControls:true,notificationEntry:action==='status-entry',productAcceptance:'not-run'}));
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
