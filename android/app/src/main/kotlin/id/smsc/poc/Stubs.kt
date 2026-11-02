package id.smsc.poc

import android.app.Activity
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.os.IBinder

// Stub minimum agar app memenuhi syarat peran SMS default (eksperimen R1: apakah batas kirim OS berubah).
// Tidak memproses SMS/MMS masuk. JANGAN dijadikan SMS default di HP pribadi.

class SmsDeliverReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) { /* sengaja kosong */ }
}

class MmsReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) { /* sengaja kosong */ }
}

class ComposeSmsActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        finish()
    }
}

class HeadlessSmsSendService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null
}
