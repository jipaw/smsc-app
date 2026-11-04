package id.smsc.poc

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.ImageFormat
import android.graphics.SurfaceTexture
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.CaptureRequest
import android.media.ImageReader
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.os.SystemClock
import android.util.Size
import android.view.Gravity
import android.view.Surface
import android.view.TextureView
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import id.smsc.poc.core.EnrollLink
import id.smsc.poc.core.QrDecoder

/**
 * Pemindai QR enrollment: Camera2 (API platform, tanpa AndroidX/Play services) + ZXing (QrDecoder di :core).
 * Hanya bidang Y frame YUV_420_888 yang dibaca; decode di thread latar, frame dilewati selama decode berjalan.
 * Hasil: RESULT_OK dengan EXTRA_TEXT berisi tautan smsc://enroll yang valid (QR lain diabaikan dengan pesan).
 */
class ScanActivity : Activity() {
    private lateinit var texture: TextureView
    private lateinit var hint: TextView
    private val bgThread = HandlerThread("qr-scan").apply { start() }
    private val bg = Handler(bgThread.looper)
    private var camera: CameraDevice? = null
    private var session: CameraCaptureSession? = null
    private var reader: ImageReader? = null
    private var yBuf = ByteArray(0)
    @Volatile private var decoding = false
    @Volatile private var done = false
    private var lastWrongQrAt = 0L

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        texture = TextureView(this)
        hint = TextView(this).apply {
            text = "Arahkan kamera ke QR di dashboard (Devices → Tambah device)"
            setPadding(32, 24, 32, 24); setBackgroundColor(0xAA000000.toInt()); setTextColor(0xFFFFFFFF.toInt()); gravity = Gravity.CENTER
        }
        val cancel = Button(this).apply { text = "Batal"; setOnClickListener { setResult(RESULT_CANCELED); finish() } }
        val bar = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; addView(hint); addView(cancel) }
        setContentView(FrameLayout(this).apply {
            addView(texture, FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT))
            addView(bar, FrameLayout.LayoutParams(FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM))
        })
    }

    override fun onResume() {
        super.onResume()
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.CAMERA), REQ_CAMERA)
            return
        }
        whenTextureReady { openCamera() }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        if (requestCode != REQ_CAMERA) return
        if (grantResults.firstOrNull() != PackageManager.PERMISSION_GRANTED) {
            hint.text = "Izin kamera ditolak. Pakai kolom 'tempel tautan / kode' di layar sebelumnya."
            return
        }
        whenTextureReady { openCamera() }
    }

    override fun onPause() {
        closeCamera()
        super.onPause()
    }

    override fun onDestroy() {
        bgThread.quitSafely()
        super.onDestroy()
    }

    private fun whenTextureReady(f: () -> Unit) {
        if (texture.isAvailable) { f(); return }
        texture.surfaceTextureListener = object : TextureView.SurfaceTextureListener {
            override fun onSurfaceTextureAvailable(st: SurfaceTexture, w: Int, h: Int) = f()
            override fun onSurfaceTextureSizeChanged(st: SurfaceTexture, w: Int, h: Int) {}
            override fun onSurfaceTextureDestroyed(st: SurfaceTexture) = true
            override fun onSurfaceTextureUpdated(st: SurfaceTexture) {}
        }
    }

    /** Kamera belakang; ukuran YUV terdekat ke 1280x720 (cukup untuk QR, ringan untuk decode). */
    private fun openCamera() {
        if (camera != null || done) return
        val cm = getSystemService(CameraManager::class.java)
        val id = cm.cameraIdList.firstOrNull { cm.getCameraCharacteristics(it).get(CameraCharacteristics.LENS_FACING) == CameraCharacteristics.LENS_FACING_BACK }
            ?: cm.cameraIdList.firstOrNull()
        if (id == null) { hint.text = "Kamera tidak tersedia"; return }
        val map = cm.getCameraCharacteristics(id).get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP) ?: return
        val size = map.getOutputSizes(ImageFormat.YUV_420_888).filter { it.width <= 1920 && it.height <= 1080 }
            .minByOrNull { Math.abs(it.width * it.height - 1280 * 720) } ?: Size(640, 480)
        val r = ImageReader.newInstance(size.width, size.height, ImageFormat.YUV_420_888, 2)
        r.setOnImageAvailableListener({ onFrame(it) }, bg)
        reader = r
        try {
            cm.openCamera(id, object : CameraDevice.StateCallback() {
                override fun onOpened(device: CameraDevice) { camera = device; startSession(device, r, size) }
                override fun onDisconnected(device: CameraDevice) { device.close(); camera = null }
                override fun onError(device: CameraDevice, error: Int) {
                    device.close(); camera = null
                    runOnUiThread { hint.text = "Kamera error ($error)" }
                }
            }, bg)
        } catch (e: SecurityException) {
            hint.text = "Izin kamera diperlukan"
        }
    }

    @Suppress("DEPRECATION") // createCaptureSession(List<Surface>, …): tersedia sejak API 21, cukup untuk minSdk 29
    private fun startSession(device: CameraDevice, r: ImageReader, size: Size) {
        val st = texture.surfaceTexture ?: return
        st.setDefaultBufferSize(size.width, size.height)
        val preview = Surface(st)
        device.createCaptureSession(listOf(preview, r.surface), object : CameraCaptureSession.StateCallback() {
            override fun onConfigured(s: CameraCaptureSession) {
                session = s
                val req = device.createCaptureRequest(CameraDevice.TEMPLATE_PREVIEW).apply {
                    addTarget(preview); addTarget(r.surface)
                    set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_PICTURE)
                }
                runCatching { s.setRepeatingRequest(req.build(), null, bg) }
            }
            override fun onConfigureFailed(s: CameraCaptureSession) { runOnUiThread { hint.text = "Kamera tidak dapat dikonfigurasi" } }
        }, bg)
    }

    private fun onFrame(r: ImageReader) {
        val img = r.acquireLatestImage() ?: return
        if (decoding || done) { img.close(); return }
        val plane = img.planes[0] // Y: pixelStride = 1 untuk YUV_420_888
        val w = img.width
        val h = img.height
        val rowStride = plane.rowStride
        val buf = plane.buffer
        if (yBuf.size < buf.remaining()) yBuf = ByteArray(buf.remaining())
        val n = buf.remaining()
        buf.get(yBuf, 0, n)
        img.close()
        decoding = true
        try {
            val text = runCatching { QrDecoder.decodeLuminance(yBuf, w, h, rowStride, cropFraction = 0.85) }.getOrNull() ?: return
            val link = runCatching { EnrollLink.parse(text, null) }.getOrNull()
            if (link == null) {
                val now = SystemClock.elapsedRealtime()
                if (now - lastWrongQrAt > 2_000) { lastWrongQrAt = now; runOnUiThread { hint.text = "QR terbaca, tetapi bukan QR enrollment SMSC" } }
                return
            }
            done = true
            runOnUiThread {
                setResult(RESULT_OK, Intent().putExtra(EXTRA_TEXT, text))
                finish()
            }
        } finally {
            decoding = false
        }
    }

    private fun closeCamera() {
        runCatching { session?.close() }; session = null
        runCatching { camera?.close() }; camera = null
        runCatching { reader?.close() }; reader = null
    }

    companion object {
        const val EXTRA_TEXT = "id.smsc.poc.QR_TEXT"
        private const val REQ_CAMERA = 31
    }
}
