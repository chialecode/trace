package com.github.chialecode.trace.cmd.trace_mobile.internal.qrscanner

import android.Manifest
import android.content.Context
import android.os.SystemClock
import android.util.Log
import android.view.ViewGroup
import android.widget.FrameLayout
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleOwner
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.ReaderException
import com.google.zxing.common.HybridBinarizer
import com.google.zxing.qrcode.QRCodeReader
import dev.mygo.Native
import dev.mygo.PluginHost
import java.util.EnumMap
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

fun makeScanner(): Scanner = AndroidScanner(Native.context)

private class AndroidScanner(context: Context) : FrameLayout(context), Scanner {
    private val previewView = PreviewView(context).apply {
        implementationMode = PreviewView.ImplementationMode.COMPATIBLE
        layoutParams = LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
    }
    private val executor: ExecutorService = Executors.newSingleThreadExecutor()
    private val decodeHints: Map<DecodeHintType, Any> = EnumMap<DecodeHintType, Any>(DecodeHintType::class.java).apply {
        put(DecodeHintType.TRY_HARDER, true)
        put(DecodeHintType.CHARACTER_SET, "UTF-8")
    }

    private var generation: Long = 0
    private var active: Boolean = false
    private var closed: Boolean = false
    private var cameraProvider: ProcessCameraProvider? = null
    private var onScanned: ((String) -> Unit)? = null
    private var onError: ((String) -> Unit)? = null

    @Volatile
    private var lastPayload: String = ""

    @Volatile
    private var lastEmitMs: Long = 0

    init {
        addView(previewView)
    }

    override fun start(onScanned: (String) -> Unit, onError: (String) -> Unit) {
        if (closed) return
        generation += 1
        val gen = generation
        active = true
        this.onScanned = onScanned
        this.onError = onError
        lastPayload = ""
        lastEmitMs = 0

        PluginHost.requestPermissions(arrayOf(Manifest.permission.CAMERA)) { granted ->
            if (!active || closed || gen != generation) return@requestPermissions
            if (!granted) {
                onError("未获得相机权限，请在下方粘贴二维码文本或手动输入。")
                return@requestPermissions
            }
            val ctx = PluginHost.context
            val future = ProcessCameraProvider.getInstance(ctx)
            future.addListener({
                if (!active || closed || gen != generation) return@addListener
                val owner = PluginHost.activity as? LifecycleOwner
                if (owner == null) {
                    onError("当前界面不支持相机预览，请粘贴二维码文本或手动输入。")
                    return@addListener
                }
                runCatching {
                    val provider = future.get()
                    val preview = Preview.Builder().build().also {
                        it.surfaceProvider = previewView.surfaceProvider
                    }
                    val analysis = ImageAnalysis.Builder()
                        .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                        .build()
                    analysis.setAnalyzer(executor) { imageProxy ->
                        try {
                            if (active && !closed && gen == generation) {
                                val decoded = decodeQR(imageProxy)
                                if (!decoded.isNullOrEmpty()) {
                                    val now = SystemClock.uptimeMillis()
                                    if (decoded != lastPayload || now - lastEmitMs >= 1500L) {
                                        lastPayload = decoded
                                        lastEmitMs = now
                                        this.onScanned?.invoke(decoded)
                                    }
                                }
                            }
                        } finally {
                            imageProxy.close()
                        }
                    }
                    provider.unbindAll()
                    provider.bindToLifecycle(owner, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis)
                    cameraProvider = provider
                }.onFailure { err ->
                    Log.w("trace-qr", "camera start failed: $err")
                    onError("相机初始化失败，可在下方粘贴二维码文本或手动输入。")
                }
            }, ContextCompat.getMainExecutor(ctx))
        }
    }

    override fun stop() {
        generation += 1
        active = false
        onScanned = null
        onError = null
        runCatching { cameraProvider?.unbindAll() }
    }

    override fun close() {
        if (closed) return
        closed = true
        stop()
        executor.shutdown()
    }

    private fun decodeQR(image: ImageProxy): String? {
        val plane = image.planes.firstOrNull() ?: return null
        val w = image.width
        val h = image.height
        if (w <= 0 || h <= 0) return null

        val buf = plane.buffer
        val rowStride = plane.rowStride
        val pixelStride = plane.pixelStride
        val yData = ByteArray(w * h)

        if (pixelStride == 1 && rowStride == w) {
            buf.get(yData, 0, yData.size)
        } else {
            val row = ByteArray(rowStride)
            for (r in 0 until h) {
                buf.position(r * rowStride)
                val toRead = minOf(buf.remaining(), rowStride)
                buf.get(row, 0, toRead)
                if (pixelStride == 1) {
                    System.arraycopy(row, 0, yData, r * w, w)
                } else {
                    for (c in 0 until w) {
                        yData[r * w + c] = row[c * pixelStride]
                    }
                }
            }
        }

        val source = PlanarYUVLuminanceSource(yData, w, h, 0, 0, w, h, false)
        val reader = QRCodeReader()
        return try {
            reader.decode(BinaryBitmap(HybridBinarizer(source)), decodeHints).text
        } catch (_: ReaderException) {
            try {
                reader.decode(BinaryBitmap(HybridBinarizer(source.invert())), decodeHints).text
            } catch (_: ReaderException) {
                null
            }
        }
    }
}
