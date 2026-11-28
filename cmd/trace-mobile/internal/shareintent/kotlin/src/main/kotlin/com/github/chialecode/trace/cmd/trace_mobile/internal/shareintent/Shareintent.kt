package com.github.chialecode.trace.cmd.trace_mobile.internal.shareintent

import android.content.Intent
import android.util.Log
import dev.mygo.Native
import dev.mygo.PluginHost
import kotlinx.coroutines.launch

private const val EXTRA_CONSUMED = "io.github.chialecode.trace.share_consumed"
private const val MAX_SHARE_CHARS = 16384

fun makeReceiver(): Receiver = AndroidShareReceiver()

private class AndroidShareReceiver : Receiver {
    private val lock = Any()
    private var active = false
    private var closed = false
    private var callback: ((String) -> Unit)? = null
    private var prevOnIntent: ((Intent) -> Unit)? = null
    private var installedHandler: ((Intent) -> Unit)? = null

    override fun start(onShared: (String) -> Unit) {
        synchronized(lock) {
            if (closed) return
            active = true
            callback = onShared
        }
        Native.onMain {
            val prev = PluginHost.onIntent
            val handler: (Intent) -> Unit = { intent ->
                runCatching { prev?.invoke(intent) }
                handleIntent(intent)
            }
            synchronized(lock) {
                if (closed || !active) return@onMain
                if (installedHandler == null) {
                    prevOnIntent = prev
                    installedHandler = handler
                    PluginHost.onIntent = handler
                }
            }
            // Check cold-start launch intent once.
            Native.activity?.intent?.let { handleIntent(it) }
        }
    }

    override fun stop() {
        val hadHandler: Boolean
        synchronized(lock) {
            active = false
            callback = null
            hadHandler = installedHandler != null
        }
        if (hadHandler) {
            Native.onMain {
                synchronized(lock) {
                    if (PluginHost.onIntent === installedHandler) {
                        PluginHost.onIntent = prevOnIntent
                    }
                    installedHandler = null
                    prevOnIntent = null
                }
            }
        }
    }

    override fun close() {
        synchronized(lock) {
            if (closed) return
            closed = true
        }
        stop()
    }

    private fun handleIntent(intent: Intent) {
        if (intent.action != Intent.ACTION_SEND) return
        val mime = intent.type ?: ""
        if (!mime.startsWith("text/", ignoreCase = true) && mime != "*/*") return
        if (intent.getBooleanExtra(EXTRA_CONSUMED, false)) return

        val extracted = extractSharedText(intent)
        if (extracted.isEmpty()) return

        // Mark consumed before emitting so activity recreation never re-submits.
        runCatching { intent.putExtra(EXTRA_CONSUMED, true) }

        val cb = synchronized(lock) {
            if (!active || closed) null else callback
        } ?: return

        Native.scope.launch {
            runCatching { cb(extracted) }.onFailure { err ->
                Log.w("trace-share", "failed to deliver shared intent text: $err")
            }
        }
    }

    private fun extractSharedText(intent: Intent): String {
        val text = intent.getStringExtra(Intent.EXTRA_TEXT)?.trim().orEmpty()
        val subject = intent.getStringExtra(Intent.EXTRA_SUBJECT)?.trim().orEmpty()
        val combined = when {
            text.isNotEmpty() && subject.isNotEmpty() && !text.contains("http://", true) && !text.contains("https://", true) &&
                (subject.contains("http://", true) || subject.contains("https://", true)) -> "$subject $text"
            text.isNotEmpty() -> text
            subject.isNotEmpty() -> subject
            else -> {
                val clip = intent.clipData
                if (clip != null && clip.itemCount > 0) {
                    runCatching { clip.getItemAt(0).coerceToText(Native.context)?.toString()?.trim().orEmpty() }.getOrDefault("")
                } else {
                    ""
                }
            }
        }
        return if (combined.length > MAX_SHARE_CHARS) combined.substring(0, MAX_SHARE_CHARS) else combined
    }
}
