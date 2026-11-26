package com.github.chialecode.trace.cmd.trace_mobile.internal.codeccheck

import android.media.MediaCodecList
import android.util.Log

fun makeChecker(): Checker = AndroidCodecChecker()

private class AndroidCodecChecker : Checker {
    override fun canDecode(mime: String, width: Long, height: Long): Boolean {
        val target = mime.trim().lowercase()
        if (target.isEmpty()) return true
        val w = width.coerceIn(0L, Int.MAX_VALUE.toLong()).toInt()
        val h = height.coerceIn(0L, Int.MAX_VALUE.toLong()).toInt()
        return try {
            val list = MediaCodecList(MediaCodecList.ALL_CODECS)
            val isVideo = target.startsWith("video/")
            for (info in list.codecInfos) {
                if (info.isEncoder) continue
                val matchedType = info.supportedTypes.firstOrNull { it.equals(target, ignoreCase = true) }
                    ?: continue
                if (!isVideo || w <= 0 || h <= 0) {
                    return true
                }
                val caps = try {
                    info.getCapabilitiesForType(matchedType)
                } catch (_: Throwable) {
                    null
                }
                val vcaps = caps?.videoCapabilities
                if (vcaps == null) {
                    return true
                }
                if (vcaps.isSizeSupported(w, h)) {
                    return true
                }
                // Some decoders declare portrait/landscape transposed bounds.
                if (vcaps.supportedWidths.contains(w) && vcaps.supportedHeights.contains(h)) {
                    return true
                }
            }
            false
        } catch (t: Throwable) {
            Log.w("trace-codeccheck", "MediaCodecList query failed for $target: $t")
            true
        }
    }

    override fun close() {
        // Stateless query wrapper; nothing to release.
    }
}
