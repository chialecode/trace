"""Windows process/job counters, including CPU used by already exited workers."""
import ctypes as C
from ctypes import wintypes as W

kernel=C.WinDLL('kernel32',use_last_error=True)
kernel.CreateJobObjectW.argtypes=[C.c_void_p,W.LPCWSTR];kernel.CreateJobObjectW.restype=W.HANDLE
kernel.AssignProcessToJobObject.argtypes=[W.HANDLE,W.HANDLE]
kernel.QueryInformationJobObject.argtypes=[W.HANDLE,C.c_int,C.c_void_p,W.DWORD,C.POINTER(W.DWORD)]
kernel.OpenProcess.argtypes=[W.DWORD,W.BOOL,W.DWORD];kernel.OpenProcess.restype=W.HANDLE
kernel.QueryFullProcessImageNameW.argtypes=[W.HANDLE,W.DWORD,W.LPWSTR,C.POINTER(W.DWORD)]
kernel.CloseHandle.argtypes=[W.HANDLE]

class Accounting(C.Structure):
    _fields_=[(n,C.c_int64) for n in ['user','system','periodUser','periodSystem']]+[(n,W.DWORD) for n in ['faults','processes','active','terminated']]

class Job:
    def __init__(self):
        self.handle=kernel.CreateJobObjectW(None,None);self.assigned=set()
        if not self.handle:raise C.WinError(C.get_last_error())
    def assign(self,pid,created=None):
        key=(pid,created)
        if key in self.assigned:return
        if pid in self.pids():self.assigned.add(key);return
        h=kernel.OpenProcess(0x101,False,pid)
        if not h:return
        try:
            if not kernel.AssignProcessToJobObject(self.handle,h):raise C.WinError(C.get_last_error())
            self.assigned.add(key)
        finally:kernel.CloseHandle(h)
    def counters(self):
        value=Accounting()
        if not kernel.QueryInformationJobObject(self.handle,1,C.byref(value),C.sizeof(value),None):raise C.WinError(C.get_last_error())
        return {'jobCpuSeconds':(value.user+value.system)/1e7,'jobTotalProcesses':value.processes,'jobActiveProcesses':value.active}
    def pids(self):
        # Two DWORD counts followed by pointer-sized process IDs, bounded by 4096.
        class Ids(C.Structure):_fields_=[('assigned',W.DWORD),('count',W.DWORD),('pids',C.c_size_t*4096)]
        value=Ids()
        if not kernel.QueryInformationJobObject(self.handle,3,C.byref(value),C.sizeof(value),None):raise C.WinError(C.get_last_error())
        if value.count>4096:raise ValueError('process budget exceeded')
        return set(value.pids[:value.count])
    def close(self):
        if self.handle:kernel.CloseHandle(self.handle);self.handle=None

def process_path(pid):
    h=kernel.OpenProcess(0x1000,False,pid)
    if not h:return None
    try:
        buf=C.create_unicode_buffer(32768);size=W.DWORD(len(buf))
        return buf.value if kernel.QueryFullProcessImageNameW(h,0,buf,C.byref(size)) else None
    finally:kernel.CloseHandle(h)
