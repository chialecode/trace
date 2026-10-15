#!/usr/bin/env python3
"""C09: inspect and execute an actual package without accounts or site requests."""
from __future__ import annotations
import argparse,json,os,queue,struct,subprocess,tempfile,threading,time,unittest
from pathlib import Path
import stage_evidence as evidence
from webview_probe import Desktop

def read_pipe_frame(stream,timeout=30):
    # A synchronous Windows named-pipe handle cannot have a permanently blocking
    # background read while the same handle writes a request. Poll available
    # bytes with the OS API, then read only that many, under a fixed deadline.
    import ctypes,msvcrt
    from ctypes import wintypes as W
    kernel=ctypes.WinDLL('kernel32',use_last_error=True)
    kernel.PeekNamedPipe.argtypes=[W.HANDLE,ctypes.c_void_p,W.DWORD,ctypes.c_void_p,ctypes.POINTER(W.DWORD),ctypes.c_void_p]
    handle=msvcrt.get_osfhandle(stream.fileno());deadline=time.monotonic()+timeout
    def exact(size):
        data=b''
        while len(data)<size:
            available=W.DWORD()
            if not kernel.PeekNamedPipe(handle,None,0,None,ctypes.byref(available),None):raise OSError(ctypes.get_last_error(),'package pipe disconnected')
            if available.value:
                block=stream.read(min(size-len(data),available.value))
                if not block:raise EOFError('package pipe ended')
                data+=block
            elif time.monotonic()>=deadline:raise TimeoutError('package pipe response timeout')
            else:time.sleep(.01)
        return data
    size=struct.unpack('<I',exact(4))[0]
    if size>1024*1024:raise ValueError('oversize frame')
    return json.loads(exact(size))

def write_frame(stream,value):
    data=json.dumps(value,ensure_ascii=False).encode('utf8')
    stream.write(struct.pack('<I',len(data))+data);stream.flush()

class Reader:
    def __init__(self,stream,lines=False):
        self.queue=queue.Queue()
        def pump():
            try:
                while True:
                    if lines:
                        raw=stream.readline()
                        if not raw:raise EOFError('process exited')
                        value=json.loads(raw)
                    else:
                        def exact(n):
                            out=b''
                            while len(out)<n:
                                chunk=stream.read(n-len(out))
                                if not chunk:raise EOFError('truncated frame')
                                out+=chunk
                            return out
                        size=struct.unpack('<I',exact(4))[0]
                        if size>1024*1024:raise ValueError('oversize frame')
                        value=json.loads(exact(size))
                    self.queue.put(value)
            except Exception as error:self.queue.put(error)
        threading.Thread(target=pump,daemon=True).start()
    def get(self,timeout=30):
        value=self.queue.get(timeout=timeout)
        if isinstance(value,Exception):raise value
        return value

class PackageTests(unittest.TestCase):
    package:Path
    def c09_minimized_embedded_webview_close_reconnect_and_full_exit(self):
        folder=self.package
        root=evidence.ROOT/'artifacts'/f'm1-webview-{time.time_ns()}'
        root.mkdir()
        profile=evidence.read(folder/'profile.example.json')
        profile.update(archive_id=root.name,data_root=str(root/'data'),archive_root=str(root/'archive'),vault_dir=str(root/'vault'),adapter_dir=str(folder/'adapters'),media_dir=str(folder/'media'))
        path=root/'profile.json';path.write_text(json.dumps(profile),encoding='utf8')
        env=dict(os.environ);env.pop('TRACE_PROFILE',None);env.pop('TRACE_DAEMON_PATH',None)
        proc=subprocess.Popen([str(folder/'trace-daemon.exe'),'start','--profile',str(path)],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env=env,creationflags=subprocess.CREATE_NO_WINDOW)
        windows=[];observations=[]
        try:
            ready=Reader(proc.stdout,lines=True).get(30)
            self.assertEqual(ready['status'],'ready')
            first=Desktop(folder/'trace-desktop.exe',path,root/'first',show_status=True);windows.append(first)
            info=first.action('status-entry',screenshot=root/'status.png');first.assert_background()
            self.assertTrue(info['notificationEntry']);self.assertTrue(info['r2AcquisitionQueries']);self.assertIn('/upgrades',info['routes'])
            self.assertEqual(info['instanceId'],ready['instance_id']);observations.append(info)
            requested=first.action('close');first.wait(timeout=10)
            self.assertIsNone(proc.poll(),'closing GUI stopped daemon');observations.append(requested)
            second=Desktop(folder/'trace-desktop.exe',path,root/'second');windows.append(second)
            info=second.action();second.assert_background();self.assertEqual(info['instanceId'],ready['instance_id']);observations.append(info)
            other=Desktop(folder/'trace-desktop.exe',path,root/'other');windows.append(other)
            self.assertEqual(other.action()['instanceId'],ready['instance_id']);other.assert_background()
            second.action('exit');second.wait();self.assertEqual(proc.wait(timeout=25),0)
            self.assertTrue(other.action('disconnected')['disconnected'])
            time.sleep(3)
            status=subprocess.run([str(folder/'trace-daemon.exe'),'status','--profile',str(path)],capture_output=True,timeout=10,env=env,creationflags=subprocess.CREATE_NO_WINDOW)
            self.assertNotEqual(status.returncode,0,'another open window restarted stopped daemon')
            other.action('close');other.wait(timeout=10)
            (root/'result.json').write_text(json.dumps({'status':'passed','observations':observations,'noForegroundInput':True,'productAcceptance':'not-run'},ensure_ascii=False,indent=2),encoding='utf8')
        finally:
            for window in windows:window.dispose()
            if proc.poll() is None:
                subprocess.run([str(folder/'trace-daemon.exe'),'shutdown','--profile',str(path)],capture_output=True,timeout=30,env=env,creationflags=subprocess.CREATE_NO_WINDOW)
                try:proc.wait(timeout=30)
                except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=10)
            proc.stdout.close()
    def c09_packaged_two_site_isolation_and_complete_inventory(self):
        folder=self.package
        self.assertEqual(evidence.validate_package(folder,evidence.inputs()),[])
        actual=evidence.inventory(folder)
        sums={}
        for line in (folder/'SHA256SUMS.txt').read_text(encoding='utf8').splitlines():
            digest,name=line.split('  ',1);evidence.contained(folder,name)
            self.assertNotIn(name,sums);sums[name]=digest
        self.assertEqual(sums,{n:s for n,s in actual.items() if n!='SHA256SUMS.txt'})
        manifest=evidence.read(folder/'manifest.json');sources=evidence.inputs()
        self.assertEqual(manifest['sourceInputs'],sources)
        self.assertEqual(manifest['productAcceptance'],'not-run')
        self.assertEqual(manifest['schema_version'],evidence.SCHEMA_VERSION)
        self.assertEqual(manifest['built_binaries'],{n:actual[n] for n in manifest['components']})
        self.assertEqual({Path(c['argv'][0]).stem for c in manifest['build_commands']},{'cargo','corepack'})
        self.assertTrue(any('trace-desktop/custom-protocol' in c['argv'] for c in manifest['build_commands']))
        for command in manifest['build_commands']:
            self.assertEqual(command['exitCode'],0)
            self.assertEqual(evidence.sha(evidence.contained(evidence.ROOT,command['log'])),command['logSha256'])
        binaries={'trace-desktop.exe','trace-daemon.exe','adapters/trace-adapter-bilibili.exe','adapters/trace-adapter-douyin.exe','media/ffmpeg.exe','media/ffprobe.exe','media/ffplay.exe'}
        self.assertEqual({n for n in actual if n.endswith('.exe')},binaries)
        for name in ['adapters/douyin-NOTICE.txt','dependency-licenses.md','media/LICENSE.txt','media/SOURCE.txt','media/verified.json']:
            self.assertIn(name,actual)
        media=evidence.read(folder/'media/verified.json')
        self.assertEqual(media['lock'],evidence.read(evidence.ROOT/'media-backend.lock.json'))
        for name,digest in media['files'].items():self.assertEqual(actual['media/'+name],digest)
        profile=evidence.read(folder/'profile.example.json')
        self.assertEqual(profile['test_allowed_hosts'],[])
        self.assertFalse(profile.get('allow_media_path',False))
        env=dict(os.environ);env.pop('TRACE_PROFILE',None)
        # Direct handshakes never trigger the host proxy. Kill one platform and
        # demand a fresh framed response from the other after the failure.
        children=[]
        try:
            for platform in ['bilibili','douyin']:
                proc=subprocess.Popen([str(folder/f'adapters/trace-adapter-{platform}.exe')],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env=env,creationflags=subprocess.CREATE_NO_WINDOW)
                reader=Reader(proc.stdout);children.append((proc,reader))
                request={'protocol_version':'0.1','request_id':'hello','method':'hello','params':{},'deadline_ms':5000}
                write_frame(proc.stdin,request);response=reader.get(10)
                self.assertTrue(response['ok']);self.assertEqual(response['result']['platform'],platform)
                self.assertTrue({'resolve_account','enumerate','observe','resolve_media','probe_live','sample_live_auxiliary'}<=set(response['result']['capabilities']))
            children[0][0].kill();children[0][0].wait(timeout=10)
            proc,reader=children[1];write_frame(proc.stdin,dict(request,request_id='after-peer-death'))
            self.assertEqual(reader.get(10)['request_id'],'after-peer-death')
        finally:
            for proc,_ in children:
                if proc.poll() is None:proc.kill()
                proc.wait(timeout=10)
                proc.stdin.close();proc.stdout.close()
        with tempfile.TemporaryDirectory(prefix='trace-package-') as temp:
            root=Path(temp);profile.update(archive_id=root.name,data_root=str(root/'data'),archive_root=str(root/'archive'),vault_dir=str(root/'vault'),adapter_dir=str(folder/'adapters'),media_dir=str(folder/'media'))
            path=root/'profile.json';path.write_text(json.dumps(profile),encoding='utf8')
            daemon=folder/'trace-daemon.exe'
            doctor=subprocess.run([str(daemon),'doctor','--profile',str(path)],capture_output=True,timeout=60,env=env,creationflags=subprocess.CREATE_NO_WINDOW)
            self.assertEqual(doctor.returncode,0);diag=json.loads(doctor.stdout)
            self.assertTrue(diag['release_check']['test_allowed_hosts_empty']);self.assertTrue(diag['media_backend']['ffprobe'])
            proc=subprocess.Popen([str(daemon),'start','--profile',str(path)],stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env=env,creationflags=subprocess.CREATE_NO_WINDOW)
            pipe=None
            try:
                ready=Reader(proc.stdout,lines=True).get(30);self.assertEqual(ready['status'],'ready')
                for attempt in range(40):
                    try:pipe=open(ready['pipe'],'r+b',buffering=0);break
                    except OSError:time.sleep(.1)
                self.assertIsNotNone(pipe)
                write_frame(pipe,{'client':'package-verifier','protocol_version':'0.1','archive_id':profile['archive_id']})
                self.assertEqual(read_pipe_frame(pipe)['frame'],'hello')
                def call(command,args):
                    rid=str(time.time_ns());write_frame(pipe,{'protocol_version':'0.1','request_id':rid,'command':command,'idempotency_key':rid,'args':args})
                    while True:
                        result=read_pipe_frame(pipe)
                        if result.get('request_id')==rid:
                            self.assertTrue(result['ok'],result.get('error'));return result['result']
                plugins=call('plugin.list',{})['items'];self.assertEqual({p['platform'] for p in plugins},{'bilibili','douyin'})
                call('plugin.set_state',{'plugin_id':'bilibili','state':'disabled'})
                plugins=call('plugin.list',{})['items'];self.assertEqual(next(p['state'] for p in plugins if p['platform']=='bilibili'),'disabled')
                self.assertNotEqual(next(p['state'] for p in plugins if p['platform']=='douyin'),'disabled')
                call('daemon.status',{});call('daemon.shutdown',{})
                self.assertEqual(proc.wait(timeout=30),0)
            finally:
                if proc.poll() is None:proc.kill();proc.wait(timeout=10)
                if pipe:pipe.close()
                proc.stdout.close()

def main():
    parser=argparse.ArgumentParser();parser.add_argument('package',type=Path);args=parser.parse_args()
    PackageTests.package=args.package.resolve()
    result=unittest.TextTestRunner(verbosity=2).run(unittest.TestSuite([PackageTests('c09_packaged_two_site_isolation_and_complete_inventory'),PackageTests('c09_minimized_embedded_webview_close_reconnect_and_full_exit')]))
    return 0 if result.wasSuccessful() else 1
if __name__=='__main__':raise SystemExit(main())
