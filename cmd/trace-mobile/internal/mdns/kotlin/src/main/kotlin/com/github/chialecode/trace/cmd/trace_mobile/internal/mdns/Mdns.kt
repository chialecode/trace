package com.github.chialecode.trace.cmd.trace_mobile.internal.mdns

import android.annotation.TargetApi
import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.util.Log
import dev.mygo.Native
import java.net.InetAddress
import java.util.ArrayDeque
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

fun makeBrowser(): Browser = AndroidMdnsBrowser(Native.context)

private class AndroidMdnsBrowser(private val context: Context) : Browser {
    private val lock = Any()
    private val executor: ExecutorService = Executors.newSingleThreadExecutor()
    private var generation: Long = 0
    private var closed = false
    private var discoveryListener: NsdManager.DiscoveryListener? = null
    private var multicastLock: WifiManager.MulticastLock? = null

    // API 34+ active ServiceInfoCallback handles so stop() unregisters every single one.
    private val infoCallbacks = ArrayList<Any>()

    // API 26-33 serialized resolve queue (NsdManager.resolveService rejects concurrent calls).
    private val resolveQueue = ArrayDeque<NsdServiceInfo>()
    private var resolvingLegacy = false

    private var onFound: ((String, Long, String, String) -> Unit)? = null
    private var onError: ((String) -> Unit)? = null

    private val nsdManager: NsdManager?
        get() = runCatching { context.getSystemService(Context.NSD_SERVICE) as? NsdManager }.getOrNull()

    override fun start(
        serviceType: String,
        onFound: (String, Long, String, String) -> Unit,
        onError: (String) -> Unit,
    ) {
        stop()
        val mgr = nsdManager
        if (mgr == null) {
            onError("当前设备不支持局域网自动发现")
            return
        }
        val svcType = normalizeServiceType(serviceType)
        val gen: Long
        synchronized(lock) {
            if (closed) return
            generation += 1
            gen = generation
            this.onFound = onFound
            this.onError = onError
        }

        acquireMulticastLock()

        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(regType: String) {}

            override fun onServiceFound(serviceInfo: NsdServiceInfo) {
                if (!isCurrent(gen)) return
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                    registerInfoCallbackV34(mgr, serviceInfo, gen)
                } else {
                    enqueueLegacyResolve(mgr, serviceInfo, gen)
                }
            }

            override fun onServiceLost(serviceInfo: NsdServiceInfo) {}

            override fun onDiscoveryStopped(serviceType: String) {}

            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                Log.w("trace-mdns", "start discovery failed: $errorCode")
                emitError(gen, "局域网自动发现启动失败（错误码 $errorCode）")
                stop()
            }

            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {
                Log.w("trace-mdns", "stop discovery failed: $errorCode")
            }
        }

        synchronized(lock) {
            if (closed || gen != generation) {
                releaseMulticastLock()
                return
            }
            discoveryListener = listener
        }

        runCatching {
            mgr.discoverServices(svcType, NsdManager.PROTOCOL_DNS_SD, listener)
        }.onFailure { err ->
            Log.w("trace-mdns", "discoverServices threw: $err")
            emitError(gen, "局域网自动发现不可用")
            stop()
        }
    }

    @TargetApi(Build.VERSION_CODES.UPSIDE_DOWN_CAKE)
    private fun registerInfoCallbackV34(mgr: NsdManager, info: NsdServiceInfo, gen: Long) {
        val cb = object : NsdManager.ServiceInfoCallback {
            override fun onServiceInfoCallbackRegistrationFailed(errorCode: Int) {
                Log.w("trace-mdns", "serviceInfoCallback registration failed: $errorCode")
            }

            override fun onServiceUpdated(resolved: NsdServiceInfo) {
                if (!isCurrent(gen)) return
                emitResolvedAddresses(gen, resolved.hostAddresses, resolved.port, resolved.attributes)
            }

            override fun onServiceLost() {}

            override fun onServiceInfoCallbackUnregistered() {}
        }
        synchronized(lock) {
            if (!isCurrent(gen)) return
            infoCallbacks.add(cb)
        }
        runCatching {
            mgr.registerServiceInfoCallback(info, executor, cb)
        }.onFailure { err ->
            Log.w("trace-mdns", "registerServiceInfoCallback failed: $err")
        }
    }

    private fun enqueueLegacyResolve(mgr: NsdManager, info: NsdServiceInfo, gen: Long) {
        val shouldStart: Boolean
        synchronized(lock) {
            if (!isCurrent(gen)) return
            resolveQueue.addLast(info)
            shouldStart = !resolvingLegacy
            if (shouldStart) {
                resolvingLegacy = true
            }
        }
        if (shouldStart) {
            pumpLegacyResolve(mgr, gen)
        }
    }

    private fun pumpLegacyResolve(mgr: NsdManager, gen: Long) {
        val next = synchronized(lock) {
            if (!isCurrent(gen)) {
                return
            }
            if (resolveQueue.isEmpty()) {
                resolvingLegacy = false
                return
            }
            resolveQueue.removeFirst()
        }

        @Suppress("DEPRECATION")
        val listener = object : NsdManager.ResolveListener {
            override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
                Log.w("trace-mdns", "resolveService failed: $errorCode")
                pumpLegacyResolve(mgr, gen)
            }

            override fun onServiceResolved(resolved: NsdServiceInfo) {
                if (isCurrent(gen)) {
                    @Suppress("DEPRECATION")
                    val host = resolved.host
                    val list = if (host != null) listOf(host) else emptyList()
                    emitResolvedAddresses(gen, list, resolved.port, resolved.attributes)
                }
                pumpLegacyResolve(mgr, gen)
            }
        }

        runCatching {
            @Suppress("DEPRECATION")
            mgr.resolveService(next, listener)
        }.onFailure { err ->
            Log.w("trace-mdns", "resolveService threw: $err")
            pumpLegacyResolve(mgr, gen)
        }
    }

    private fun emitResolvedAddresses(
        gen: Long,
        addrs: List<InetAddress>,
        port: Int,
        attrs: Map<String, ByteArray?>?,
    ) {
        if (port <= 0 || port > 65535 || addrs.isEmpty()) return
        val cb = synchronized(lock) {
            if (!isCurrent(gen)) null else onFound
        } ?: return

        val node = attrString(attrs, "node")
        val channel = attrString(attrs, "channel")
        for (addr in addrs) {
            val rawHost = addr.hostAddress ?: continue
            val host = rawHost.substringBefore('%').trim()
            if (host.isEmpty()) continue
            runCatching { cb(host, port.toLong(), node, channel) }
        }
    }

    private fun attrString(attrs: Map<String, ByteArray?>?, key: String): String {
        val bytes = attrs?.get(key) ?: return ""
        return runCatching { String(bytes, Charsets.UTF_8).trim() }.getOrDefault("")
    }

    private fun emitError(gen: Long, msg: String) {
        val cb = synchronized(lock) {
            if (!isCurrent(gen)) null else onError
        } ?: return
        runCatching { cb(msg) }
    }

    private fun isCurrent(gen: Long): Boolean = synchronized(lock) { !closed && gen == generation }

    override fun stop() {
        val mgr = nsdManager
        val listener: NsdManager.DiscoveryListener?
        val cbs: List<Any>
        synchronized(lock) {
            generation += 1
            listener = discoveryListener
            discoveryListener = null
            cbs = ArrayList(infoCallbacks)
            infoCallbacks.clear()
            resolveQueue.clear()
            resolvingLegacy = false
            onFound = null
            onError = null
        }

        if (mgr != null && listener != null) {
            runCatching { mgr.stopServiceDiscovery(listener) }
        }
        if (mgr != null && Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            for (cb in cbs) {
                runCatching {
                    mgr.unregisterServiceInfoCallback(cb as NsdManager.ServiceInfoCallback)
                }
            }
        }
        releaseMulticastLock()
    }

    override fun close() {
        synchronized(lock) {
            if (closed) return
            closed = true
        }
        stop()
        executor.shutdownNow()
    }

    private fun acquireMulticastLock() {
        runCatching {
            val wm = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager
                ?: return
            val ml = wm.createMulticastLock("trace-companion-mdns").apply {
                setReferenceCounted(false)
                acquire()
            }
            synchronized(lock) {
                multicastLock?.let { old -> runCatching { if (old.isHeld) old.release() } }
                multicastLock = ml
            }
        }
    }

    private fun releaseMulticastLock() {
        val ml = synchronized(lock) {
            val cur = multicastLock
            multicastLock = null
            cur
        }
        if (ml != null) {
            runCatching { if (ml.isHeld) ml.release() }
        }
    }

    private fun normalizeServiceType(raw: String): String {
        val t = raw.trim().removeSuffix(".local.").removeSuffix(".local")
        return if (t.endsWith(".")) t else "$t."
    }
}
