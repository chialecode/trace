// Operates only on the isolated packaged TRACE WebView's loopback CDP port.
// The input file contains source links and opaque credential IDs, never cookies.
import {readFile,writeFile} from 'node:fs/promises';
import {setTimeout as delay} from 'node:timers/promises';
const [portText,inputPath,outputPath]=process.argv.slice(2);const port=Number(portText);
if(!Number.isInteger(port)||port<1024||port>65535)throw Error('invalid isolated port');
const action=JSON.parse(await readFile(inputPath,'utf8'));
let target;for(let i=0;i<150;i++){try{const pages=await(await fetch(`http://127.0.0.1:${port}/json/list`,{signal:AbortSignal.timeout(1000)})).json();target=pages.find(p=>p.type==='page'&&/^(https?:\/\/tauri\.localhost|tauri:\/\/localhost)(\/|$)/.test(p.url));if(target)break;}catch{}await delay(200);}
if(!target)throw Error('isolated packaged page unavailable');
const endpoint=new URL(target.webSocketDebuggerUrl);if(!['127.0.0.1','localhost'].includes(endpoint.hostname)||Number(endpoint.port)!==port)throw Error('unexpected debug endpoint');
const socket=new WebSocket(endpoint);await new Promise((yes,no)=>{socket.addEventListener('open',yes,{once:true});socket.addEventListener('error',no,{once:true});});
let serial=0;const pending=new Map();
socket.addEventListener('message',e=>{const v=JSON.parse(e.data);const p=pending.get(v.id);if(p){pending.delete(v.id);clearTimeout(p.timer);if(v.error)p.no(Error('isolated CDP command failed'));else p.yes(v.result);}});
function command(method,params={}){return new Promise((yes,no)=>{const id=++serial;const timer=setTimeout(()=>{pending.delete(id);no(Error('isolated CDP timeout'));},45000);pending.set(id,{yes,no,timer});socket.send(JSON.stringify({id,method,params}));});}
async function evaluate(expression){const v=await command('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(v.exceptionDetails)throw Error('packaged page operation failed');return v.result.value;}
async function waitFor(expression,label,seconds=35){for(let i=0;i<seconds*5;i++){if(await evaluate(expression))return;await delay(200);}throw Error(label);}
const literal=v=>JSON.stringify(v);
try{
  await waitFor("Boolean(window.__TAURI_INTERNALS__ && document.querySelector('#app')?.children.length)",'packaged app did not mount');
  await waitFor("Boolean(document.querySelector('main')?.firstElementChild)",'initial route did not render');
  await evaluate("document.querySelector('a[aria-label=\"链接归档\"]').click()");await waitFor("Boolean(document.querySelector('.source-acquisition input'))",'source page missing');
  let result;
  if(action.mode==='inspect'){
    result={mode:'inspect',sourcePage:true,inputs:await evaluate("document.querySelectorAll('.source-acquisition input').length")};
  }else if(action.mode==='acquire'){
    await evaluate(`(()=>{const i=document.querySelector('input[aria-label="视频或直播链接"]');i.value=${literal(action.url)};i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
    if(action.credentialId){await waitFor(`Array.from(document.querySelector('select[aria-label="获取身份"]').options).some(o=>o.value===${literal(action.credentialId)})`,'credential choice missing');await evaluate(`(()=>{const i=document.querySelector('select[aria-label="获取身份"]');i.value=${literal(action.credentialId)};i.dispatchEvent(new Event('change',{bubbles:true}));})()`);}
    await evaluate("Array.from(document.querySelectorAll('button')).find(b=>b.innerText.includes('解析可得选项')).click()");
    await waitFor("Boolean(document.querySelector('.source-preview')) || Boolean(document.querySelector('[data-preview-failed=true]'))",'source preview timed out',45);
    if(!await evaluate("Boolean(document.querySelector('.source-preview'))"))throw Error('source preview refused: '+await evaluate("document.querySelector('[data-preview-failed=true]')?.textContent"));
    const offers=await evaluate("Array.from(document.querySelectorAll('.source-choice select')).map(s=>({label:s.getAttribute('aria-label'),options:Array.from(s.options).map(o=>({id:o.value,label:o.textContent}))}))");
    const selected=[];
    for(const group of offers){
      let choice;
      if(group.label.startsWith('选择封面')||group.label.startsWith('选择字幕'))choice=group.options.find(o=>o.id==='');
      else if(group.label.startsWith('选择视频')&&action.width)choice=group.options.find(o=>o.label.includes(`${action.width} × ${action.height}`)&&(!action.codec||o.label.includes(action.codec)));
      else if(group.label.startsWith('选择直播流')){
        const streams=group.options.filter(o=>o.id&&o.label.includes('FLV'));
        choice=streams.find(o=>o.label.includes(action.quality))??streams.sort((a,b)=>Number(b.label.match(/^(\d+)/)?.[1]??0)-Number(a.label.match(/^(\d+)/)?.[1]??0)||Number(b.label.includes('avc'))-Number(a.label.includes('avc')))[0];
      }
      else if(group.label.startsWith('选择必要音轨'))choice=group.options.find(o=>o.label.includes('mp4a'))??group.options.find(o=>o.id);
      else choice=group.options.find(o=>o.id);
      if(!choice)throw Error('required requested source choice unavailable');
      await evaluate(`(()=>{const s=Array.from(document.querySelectorAll('.source-choice select')).find(s=>s.getAttribute('aria-label')===${literal(group.label)});s.value=${literal(choice.id)};s.dispatchEvent(new Event('change',{bubbles:true}));})()`);
      if(choice.id)selected.push(choice.id);
    }
    const before=await evaluate("window.__TAURI_INTERNALS__.invoke('source_list')");const existing=new Set(before.items.filter(i=>i.job_id).map(i=>i.request_id));
    await evaluate("Array.from(document.querySelectorAll('.source-preview button')).find(b=>/下载所选资源|开始录制/.test(b.innerText)).click()");
    await waitFor("document.querySelector('[role=status]')?.innerText.includes('已创建一次性任务') || document.querySelector('[role=status]')?.innerText.includes('选择未被接受')",'source confirm timed out');
    const after=await evaluate("window.__TAURI_INTERNALS__.invoke('source_list')");const request=after.items.find(i=>i.job_id&&!existing.has(i.request_id));if(!request)throw Error('source confirmation was not accepted');
    result={mode:'acquire',requestId:request.request_id,guiSelection:true,selectedIds:selected,offers};
  }else if(action.mode==='stop'){
    const selector=`[data-source-request="${action.requestId}"]`;
    await waitFor(`Boolean(document.querySelector(${literal(selector)}))`,'recording row unavailable');
    await evaluate(`Array.from(document.querySelector(${literal(selector)}).querySelectorAll('button')).find(b=>b.innerText.includes('停止并封片')).click()`);
    result={mode:'stop',requestId:action.requestId,guiStop:true};
  }else if(action.mode==='read'){
    await evaluate(`location.hash=${literal('#/?entry='+encodeURIComponent(action.entryId))}`);
    await waitFor("Boolean(document.querySelector('.home-layout'))",'offline home not rendered');
    const detail=await evaluate(`window.__TAURI_INTERNALS__.invoke('archive_entry_get',{entryId:${literal(action.entryId)}})`);
    if(!detail||detail.entry?.entry_id!==action.entryId)throw Error('offline entry unavailable');
    result={mode:'read',entryId:action.entryId,offlineRead:true};
  }else throw Error('unknown source action');
  await writeFile(outputPath,JSON.stringify(result,null,2)+'\n');
  console.log(JSON.stringify({mode:action.mode,status:'passed'}));
}finally{
  if(socket.readyState!==WebSocket.CLOSED)await new Promise(resolve=>{const timer=setTimeout(resolve,2000);socket.addEventListener('close',()=>{clearTimeout(timer);resolve();},{once:true});socket.close();});
}
