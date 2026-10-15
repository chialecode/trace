"""Pure derivation from recorded counters; shared by measurement and C10 gate."""
from __future__ import annotations
import math

def percentile(values,fraction):
    # Nearest-rank percentile: for the four cold queries p95 includes the slowest.
    return sorted(values)[max(0,min(len(values)-1,math.ceil(len(values)*fraction)-1))] if values else None

def derive(raw):
    samples=raw['samples'];workload=raw['workload'];logical=raw['logicalProcessors']
    if not samples or not isinstance(logical,int) or logical<1:raise ValueError('missing counters')
    previous=None;derived=[];queries=[];explorer=[];query_seen=set()
    for sample in samples:
        elapsed=sample['elapsedSeconds'];cpu=sample['jobCpuSeconds'];processes=sample['processes']
        if not processes or not math.isfinite(elapsed) or not math.isfinite(cpu):raise ValueError('invalid sample')
        if previous is not None and (elapsed<=previous[0] or elapsed-previous[0]>15 or cpu<previous[1]):raise ValueError('discontinuous counters')
        usage=(cpu-previous[1])/(elapsed-previous[0])/logical*100 if previous is not None else 0
        previous=(elapsed,cpu)
        memory=sum(p['privateWorkingSet'] for p in processes)
        handles=sum(p['handles'] for p in processes)
        if any(not isinstance(p['privateWorkingSet'],int) or p['privateWorkingSet']<0 or not isinstance(p['handles'],int) or p['handles']<0 for p in processes):raise ValueError('invalid process counters')
        progress=sample.get('workload',{});key=progress.get('elapsedSeconds')
        if key not in query_seen:
            queries+=progress.get('queryTimesMs',[]);explorer+=progress.get('explorerTimesMs',[]);query_seen.add(key)
        derived.append(dict(sample,cpu=usage,memory=memory,handles=handles))
    if any(not isinstance(q,(int,float)) or not math.isfinite(q) or q<0 for q in queries+explorer):raise ValueError('invalid query sample')
    round_size=workload.get('explorerQueriesPerRound',0)
    idle=[s for s in derived if s.get('workload',{}).get('phase')=='idle' and s['workload'].get('elapsedSeconds',0)>=120]
    recording=[s for s in derived if s.get('workload',{}).get('phase')=='four-recordings' and s['workload'].get('elapsedSeconds',0)>=workload['idleSeconds']+60]
    mean=lambda rows:sum(s['cpu'] for s in rows)/len(rows) if rows else None
    lifecycle=raw['lifecycle'];starts=lifecycle['recordingStarts']
    delays=[s['firstReceiveUnix']-s['detectedUnix'] for s in starts]
    discovery=[s['detectedUnix']-s['observableUnix'] for s in starts]
    if any(v<0 or not math.isfinite(v) for v in discovery):raise ValueError('invalid observable timestamps')
    if any(s['detectedUnix']-s['observableUnix']>1.2*s['pollIntervalSeconds']+10 for s in starts):raise ValueError('observable-to-durable detection target failed')
    if any(v<0 or not math.isfinite(v) for v in delays):raise ValueError('invalid recording timestamps')
    closes=[e['releasedElapsed']-e['requestedElapsed'] for e in lifecycle['gui'] if e['mode']=='closed']
    recovery=lifecycle.get('recovery',{})
    measurements={
        'privateWorkingSet':max(s['memory'] for s in derived),'cpuAllProcesses':mean(derived),'handles':max(s['handles'] for s in derived),
        'queryP95Ms':percentile(queries,.95),'queryP99Ms':percentile(queries,.99),'hotQueryP95Ms':percentile(queries[4:],.95),'coldQueryP95Ms':percentile(queries[:4],.95),
        'explorerHotP95Ms':percentile(explorer[round_size:],.95),'explorerColdP95Ms':percentile(explorer[:round_size],.95),
        'throughputBytes':max(s.get('workload',{}).get('throughputBytes',0) for s in derived),
        'queueDepth':max(s.get('workload',{}).get('jobs',{}).get('queueDepth',0) for s in derived),
        'freeDiskBytes':min(s['freeDiskBytes'] for s in derived),'idleCpuMeanPct':mean(idle),'idleMemoryP95MiB':percentile([s['memory']/2**20 for s in idle],.95),
        'recordingOnlyCpuMeanPct':mean(recording),'recordingOnlyMemoryP95MiB':percentile([s['memory']/2**20 for s in recording],.95),
        'discoveryP95Seconds':percentile(discovery,.95),'startRecordingP95Seconds':percentile(delays,.95),'guiCloseSeconds':max(closes) if closes else None,
        'recoverySeconds':recovery.get('finishedElapsed',0)-recovery.get('startedElapsed',0) if recovery else None,
    }
    # The run lasts until the workload says it ended. The last 5-second sample is taken before
    # that, so it alone would shorten the run by up to one interval; the final progress the
    # harness read after the process ended may extend it, but not past the sampled run.
    sampled_end=max(s.get('workload',{}).get('elapsedSeconds',0) for s in derived)
    final_end=raw.get('finalWorkload',{}).get('elapsedSeconds',0)
    if not isinstance(final_end,(int,float)) or not math.isfinite(final_end) or final_end>sampled_end+15:raise ValueError('final progress outside the sampled run')
    return {'measurements':measurements,'durationSeconds':max(sampled_end,final_end)-workload['idleSeconds'],
        'metadataEntries':workload['metadataEntries'],'mediaMetadata':workload['mediaMetadata'],
        'explorerEntries':workload.get('explorerEntries',0),
        **{key:workload[key] for key in ['bodyMedianChars','ordinaryCheckTargets','liveCheckTargets','httpLatencyMs','queryPageSize','streamBytesPerSecond']},
        'mixedParallelSamples':sum(1 for s in derived if s.get('workload',{}).get('phase')=='mixed' and s['workload'].get('jobs',{}).get('activeRecordings',0)>=2 and s['workload'].get('jobs',{}).get('activeDownloads',0)>=2),
        'parallelRecordings':max(s.get('workload',{}).get('jobs',{}).get('activeRecordings',0) for s in derived),
        'parallelDownloads':max(s.get('workload',{}).get('jobs',{}).get('activeDownloads',0) for s in derived),
        'recordingOnlyStreams':max((s['workload'].get('jobs',{}).get('activeRecordings',0) for s in recording),default=0),
        'guiModes':sorted(set(e['mode'] for e in lifecycle['gui'])),
        'idleWindowSeconds':idle[-1]['elapsedSeconds']-idle[0]['elapsedSeconds'] if idle else 0,
        'recordingStarts':len(starts),'jobErrors':lifecycle.get('jobErrors'),
    }
