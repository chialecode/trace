#!/usr/bin/env python3
"""Measure an isolated synthetic workload; raw Windows counters, never targets.

Use --idle 1020 --seconds 3600 --entries 100000 for the required workload.
Short smoke runs deliberately cannot pass evidence.py.
"""
from __future__ import annotations
import argparse,ctypes as C,json,os,subprocess,time,threading,platform,traceback
from ctypes import wintypes as W
from pathlib import Path
import evidence
from windows_metrics import Job,process_path
from benchmark_metrics import derive
import shutil
from webview_probe import Desktop

def probe_gui(exe,profile,root,result,windows,stop):
    """Exercise actual packaged pages while the counter sampler keeps running."""
    try:
        for index in range(2):
            if stop.is_set():return
            window=Desktop(exe,profile,root/f'probe-{index}');windows.append(window)
            info=window.action();window.assert_background()
            result.append(info)
            if stop.wait(15):return # Allow independent 5s process snapshots.
            action=window.action('close')
            with (root/'gui-actions.jsonl').open('a',encoding='utf8') as stream:stream.write(json.dumps(action)+'\n')
            window.wait(timeout=10)
            if stop.wait(15):return
    except Exception as error:result.append({'error':str(error)})
    finally:
        (root/'gui-probe.json').write_text(json.dumps(result,ensure_ascii=False,indent=2),encoding='utf8')

class ProcessEntry(C.Structure):
    _fields_=[('size',W.DWORD),('usage',W.DWORD),('pid',W.DWORD),('heap',C.c_size_t),('module',W.DWORD),('threads',W.DWORD),('parent',W.DWORD),('priority',W.LONG),('flags',W.DWORD),('exe',W.WCHAR*260)]
class Memory(C.Structure):
    _fields_=[('cb',W.DWORD),('faults',W.DWORD)]+[(n,C.c_size_t) for n in ['peakWorkingSet','workingSet','peakPaged','paged','peakNonPaged','nonPaged','pagefile','peakPagefile','privateCommit','privateWorkingSet','sharedCommit']]

kernel=C.WinDLL('kernel32',use_last_error=True);psapi=C.WinDLL('psapi',use_last_error=True)
kernel.OpenProcess.argtypes=[W.DWORD,W.BOOL,W.DWORD];kernel.OpenProcess.restype=W.HANDLE
kernel.CreateToolhelp32Snapshot.argtypes=[W.DWORD,W.DWORD];kernel.CreateToolhelp32Snapshot.restype=W.HANDLE
kernel.Process32FirstW.argtypes=[W.HANDLE,C.POINTER(ProcessEntry)];kernel.Process32NextW.argtypes=kernel.Process32FirstW.argtypes
kernel.CloseHandle.argtypes=[W.HANDLE]
kernel.GetProcessTimes.argtypes=[W.HANDLE,C.POINTER(W.FILETIME),C.POINTER(W.FILETIME),C.POINTER(W.FILETIME),C.POINTER(W.FILETIME)]
kernel.GetProcessHandleCount.argtypes=[W.HANDLE,C.POINTER(W.DWORD)]
psapi.GetProcessMemoryInfo.argtypes=[W.HANDLE,C.POINTER(Memory),W.DWORD]

def processes(roots:set[int],executable:Path|None=None)->list[dict]:
    handle=kernel.CreateToolhelp32Snapshot(2,0);entry=ProcessEntry();entry.size=C.sizeof(entry);rows={}
    try:
        ok=kernel.Process32FirstW(handle,C.byref(entry))
        while ok:
            rows[entry.pid]=(entry.parent,entry.exe);ok=kernel.Process32NextW(handle,C.byref(entry))
    finally:kernel.CloseHandle(handle)
    owned=set(roots)
    if executable:
        owned|={pid for pid,(_,name) in rows.items() if name.lower()==executable.name.lower() and (value:=process_path(pid)) and Path(value).resolve()==executable.resolve()}
    while True:
        new={pid for pid,(parent,_) in rows.items() if parent in owned}
        if new<=owned:break
        owned|=new
    out=[]
    for pid in sorted(owned&rows.keys()):
        h=kernel.OpenProcess(0x410,False,pid)
        if not h:continue # Child may have exited between enumeration and open.
        try:
            mem=Memory();mem.cb=C.sizeof(mem);handles=W.DWORD();created,exit_,system,user=(W.FILETIME() for _ in range(4))
            if not psapi.GetProcessMemoryInfo(h,C.byref(mem),C.sizeof(mem)):raise OSError(C.get_last_error(),'GetProcessMemoryInfo')
            if not kernel.GetProcessTimes(h,C.byref(created),C.byref(exit_),C.byref(system),C.byref(user)):raise OSError(C.get_last_error(),'GetProcessTimes')
            if not kernel.GetProcessHandleCount(h,C.byref(handles)):raise OSError(C.get_last_error(),'GetProcessHandleCount')
            ticks=lambda v:(v.dwHighDateTime<<32)|v.dwLowDateTime
            out.append({'pid':pid,'name':rows[pid][1],'createdTicks':ticks(created),'cpuSeconds':(ticks(system)+ticks(user))/1e7,'privateWorkingSet':mem.privateWorkingSet,'workingSet':mem.workingSet,'privateCommit':mem.privateCommit,'handles':handles.value})
        finally:kernel.CloseHandle(h)
    return out

def main()->int:
    parser=argparse.ArgumentParser();parser.add_argument('directory',type=Path);parser.add_argument('--idle',type=int,default=1030);parser.add_argument('--seconds',type=int,default=3600);parser.add_argument('--entries',type=int,default=100000);parser.add_argument('--package',type=Path);args=parser.parse_args()
    root=args.directory.resolve()
    if root.exists():parser.error('choose a new directory; evidence is never overwritten')
    if args.package and evidence.read(args.package/'manifest.json')['sourceInputs']!=evidence.inputs():parser.error('package source inputs are stale')
    root.parent.mkdir(parents=True,exist_ok=True);before=evidence.inputs()
    binary=evidence.ROOT/'target/measure/resource_bench.exe'
    build_log=root.with_suffix('.build.log')
    if build_log.exists():parser.error('choose a new build log path')
    command=['cargo','build','--locked','--profile','measure','-p','trace-e2e','--bin','resource_bench','--bin','bench_adapter']
    with build_log.open('w',encoding='utf8') as log:build=subprocess.run(command,cwd=evidence.ROOT,stdout=log,stderr=subprocess.STDOUT)
    if build.returncode or before!=evidence.inputs():return 1
    binaries={p.name:evidence.sha(p) for p in [binary,binary.with_name('bench_adapter.exe')]}
    gate=root.with_suffix('.go')
    if gate.exists():parser.error('stale start gate')
    samples=[];start=time.monotonic();started_unix=time.time();gui_events=[];gui_open=False;gui_open_count=0;gui_prepared=False
    gui_exe=root/'gui/trace-desktop.exe';job=Job();gui_thread=None;gui_results=[];gui_windows=[];errors=[];gui_stop=threading.Event()
    with root.with_suffix('.log').open('w',encoding='utf8') as log:
        proc=subprocess.Popen([str(binary),str(root),str(args.idle),str(args.seconds),str(args.entries),str(gate)],cwd=evidence.ROOT,stdout=log,stderr=subprocess.STDOUT,creationflags=subprocess.CREATE_NO_WINDOW)
        try:
            job.assign(proc.pid);gate.write_text('assigned',encoding='utf8')
            while proc.poll() is None:
                tick=time.monotonic()
                try:progress=evidence.read(root/'progress.json')
                except (OSError,ValueError):progress={}
                if args.package and not gui_prepared and (root/'ready.json').exists():
                    gui_exe.parent.mkdir();shutil.copy2(args.package/'trace-desktop.exe',gui_exe);shutil.copy2(root/'profile.json',gui_exe.parent/'profile.json');gui_prepared=True
                if gui_prepared and gui_thread is None and progress.get('phase')=='mixed':
                    gui_thread=threading.Thread(target=probe_gui,args=(gui_exe,root/'profile.json',root,gui_results,gui_windows,gui_stop),daemon=True);gui_thread.start()
                # Every GUI owns a separate job from process creation. Never
                # reassign WebView's nested/sandbox jobs to a different parent.
                # Keep finished jobs open until the final sample so their CPU
                # usage remains included, including already exited children.
                window_jobs=[window.job for window in list(gui_windows)]
                gui_ids=set().union(*(group.pids() for group in window_jobs))
                gui=processes(gui_ids)
                if gui and progress.get('desktopClients',0)>0 and not gui_open:
                    gui_events.append({'mode':'open' if gui_open_count==0 else 'reconnected','readyElapsed':tick-start,'desktopConnections':progress['desktopConnections']});gui_open=True;gui_open_count+=1
                if gui_open and not gui and progress.get('desktopClients')==0:
                    actions=[json.loads(line) for line in (root/'gui-actions.jsonl').read_text(encoding='utf8').splitlines()] if (root/'gui-actions.jsonl').exists() else []
                    closes=[a for a in actions if a.get('action')=='close']
                    if len(closes)<gui_open_count:raise ValueError('GUI close lacks recorded action timestamp')
                    gui_events.append({'mode':'closed','requestedElapsed':closes[gui_open_count-1]['unix']-started_unix,'releasedElapsed':tick-start});gui_open=False
                groups=[job,*window_jobs];counters=[group.counters() for group in groups]
                totals={key:sum(counter[key] for counter in counters) for key in ['jobCpuSeconds','jobTotalProcesses','jobActiveProcesses']}
                stats=processes(job.pids()|gui_ids)
                if stats:samples.append({'elapsedSeconds':tick-start,'processes':stats,**totals,'freeDiskBytes':shutil.disk_usage(root.parent).free,'workload':progress})
                time.sleep(max(.05,5-(time.monotonic()-tick)))
        except Exception as error:
            # Preserve partial counters and an explicit failed result as well as
            # the diagnostic log when the sampler itself fails.
            traceback.print_exc()
            errors.append(f'counter/lifecycle failure: {type(error).__name__}')
        finally:
            if proc.poll() is None:proc.terminate();proc.wait(timeout=15)
            gui_stop.set()
            if gui_thread:gui_thread.join(timeout=55)
            for window in gui_windows:window.dispose()
            job.close()
            if gate.exists():gate.unlink()
    workload=evidence.read(root/'ready.json') if (root/'ready.json').exists() else {}
    try:final_workload=evidence.read(root/'progress.json')
    except (OSError,ValueError):final_workload={}
    lifecycle=evidence.read(root/'lifecycle.json') if (root/'lifecycle.json').exists() else {'recordingStarts':[]}
    lifecycle['gui']=gui_events
    lifecycle['guiProbe']=gui_results
    raw_data={'samples':samples,'workload':workload,'finalWorkload':final_workload,'lifecycle':lifecycle,'logicalProcessors':os.cpu_count() or 1,'device':{'os':platform.platform(),'processor':os.environ.get('PROCESSOR_IDENTIFIER','unknown'),'architecture':platform.machine()},'guiExecutableSha256':evidence.sha(gui_exe) if gui_exe.exists() else None,'startedUnix':started_unix,'build':{'argv':command,'exitCode':build.returncode,'log':build_log.relative_to(evidence.ROOT).as_posix(),'logSha256':evidence.sha(build_log),'binaries':binaries},'sourceInputs':before}
    raw=root/'resource-samples.json';raw.write_text(json.dumps(raw_data,ensure_ascii=False),encoding='utf8')
    try:summary=derive(raw_data)
    except (ValueError,KeyError,TypeError,IndexError) as error:summary={};errors.append(str(error))
    limits={'idleCpuMeanPct':1,'idleMemoryP95MiB':150,'hotQueryP95Ms':500,'coldQueryP95Ms':1500,'explorerHotP95Ms':500,'explorerColdP95Ms':1500,'recordingOnlyCpuMeanPct':5,'recordingOnlyMemoryP95MiB':300,'startRecordingP95Seconds':10,'recoverySeconds':30,'guiCloseSeconds':10}
    for key,limit in limits.items():
        value=summary.get('measurements',{}).get(key)
        if not isinstance(value,(int,float)) or value<0 or value>limit:errors.append('unproved or failed '+key)
    for key,minimum in {'durationSeconds':3600,'metadataEntries':100000,'mediaMetadata':500000,'bodyMedianChars':500,'ordinaryCheckTargets':100,'liveCheckTargets':20,'httpLatencyMs':100,'queryPageSize':50,'explorerEntries':100000,'streamBytesPerSecond':1000000,'parallelDownloads':2,'parallelRecordings':2,'recordingOnlyStreams':4,'mixedParallelSamples':1,'idleWindowSeconds':900,'recordingStarts':4}.items():
        if summary.get(key,0)<minimum:errors.append('missing/short '+key)
    if summary.get('guiModes')!=['closed','open','reconnected']:errors.append('incomplete GUI lifecycle')
    if len(gui_results)!=2 or any(r.get('embeddedPage') is not True for r in gui_results):errors.append('packaged WebView probe failed')
    if summary.get('jobErrors')!=0:errors.append('workload job errors')
    if before!=evidence.inputs():errors.append('source changed')
    if proc.returncode!=0:errors.append('workload process failed')
    result={'status':'failed' if errors else 'passed','exitCode':proc.returncode,'sourceInputs':before,'sourceUnchanged':before==evidence.inputs(),**summary,'samples':raw.relative_to(evidence.ROOT).as_posix(),'samplesSha256':evidence.sha(raw),'errors':errors,'limitations':['Synthetic source and harness CPU included in Windows job accounting, including exited child workers','Resource snapshots every 5 seconds; minimized packaged WebView driven via loopback CDP with no foreground input','Recovery measures complete Core teardown/reopen; process crash correctness is separately tested','No extrapolation to real account archives']}
    (root/'benchmark.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf8')
    print(json.dumps(result['errors'],ensure_ascii=False));print(json.dumps(result.get('measurements',{}),ensure_ascii=False));return 1 if errors else 0

if __name__=='__main__':raise SystemExit(main())
