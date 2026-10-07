"""Isolated, minimized packaged WebView probe. No native input automation."""
from __future__ import annotations
import ctypes,json,os,socket,subprocess,time
from ctypes import wintypes as W
from pathlib import Path
from windows_metrics import Job

class Desktop:
    def __init__(self,exe:Path,profile:Path,root:Path,show_status=False):
        root.mkdir(parents=True,exist_ok=True)
        self.root=root
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0));self.port=sock.getsockname()[1]
        env=dict(os.environ)
        env.pop('TRACE_PROFILE',None);env.pop('TRACE_DAEMON_PATH',None)
        env['WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS']=f'--remote-debugging-port={self.port} --remote-debugging-address=127.0.0.1'
        env['WEBVIEW2_USER_DATA_FOLDER']=str(root/'webview-data')
        self.log=(root/'desktop.log').open('w',encoding='utf8')
        argv=[str(exe),'--profile',str(profile),'--minimized']
        if show_status:argv.append('--show-status')
        self.proc=subprocess.Popen(argv,env=env,cwd=exe.parent,stdout=self.log,stderr=subprocess.STDOUT,creationflags=subprocess.CREATE_NO_WINDOW)
        self.job=Job();self.job.assign(self.proc.pid)
    def action(self,action='inspect',screenshot:Path|None=None):
        argv=['node',str(Path(__file__).with_suffix('.mjs')),str(self.port),action]
        if screenshot:argv.append(str(screenshot))
        result=subprocess.run(argv,capture_output=True,text=True,encoding='utf8',timeout=55,creationflags=subprocess.CREATE_NO_WINDOW)
        (self.root/f'{action}-{time.time_ns()}.log').write_text(result.stdout+'\n'+result.stderr,encoding='utf8')
        if result.returncode:raise AssertionError(f'WebView {action} failed: {result.stderr[-1500:]}')
        return json.loads(result.stdout)
    def assert_background(self):
        user=ctypes.WinDLL('user32',use_last_error=True)
        user.GetForegroundWindow.restype=W.HWND
        user.GetWindowThreadProcessId.argtypes=[W.HWND,ctypes.POINTER(W.DWORD)]
        user.IsIconic.argtypes=[W.HWND];user.IsIconic.restype=W.BOOL
        pid=W.DWORD();user.GetWindowThreadProcessId(user.GetForegroundWindow(),ctypes.byref(pid))
        if pid.value==self.proc.pid:raise AssertionError('probe took foreground focus')
        windows=[]
        callback=ctypes.WINFUNCTYPE(W.BOOL,W.HWND,W.LPARAM)
        @callback
        def collect(hwnd,_):
            owner=W.DWORD();user.GetWindowThreadProcessId(hwnd,ctypes.byref(owner))
            if owner.value==self.proc.pid and user.IsIconic(hwnd):windows.append(int(hwnd))
            return True
        user.EnumWindows.argtypes=[callback,W.LPARAM];user.EnumWindows(collect,0)
        if not windows:raise AssertionError('probe window is not minimized')
    def wait(self,timeout=25):
        start=time.monotonic()
        status=self.proc.wait(timeout=timeout)
        if status!=0:raise AssertionError(f'desktop exit status {status}')
        while self.job.pids():
            if time.monotonic()-start>=timeout:raise AssertionError('desktop child resources remain')
            time.sleep(.1)
    def dispose(self):
        if self.proc.poll() is None:self.proc.kill();self.proc.wait(timeout=10)
        self.log.close()
        self.job.close()
