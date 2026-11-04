package id.smsc.poc

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.telephony.SmsMessage

/** Menerima sentIntent/deliveryIntent per part. Terdaftar di manifest. */
class SmsResultReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        PocRuntime.init(context)
        val data = intent.data ?: return
        val messageId = data.host ?: return
        val seg = data.pathSegments
        if (seg.size < 3) return
        val attempt = seg[0].toIntOrNull() ?: return
        val part = seg[1].toIntOrNull() ?: return
        val kind = seg[2]
        val code = resultCode

        when (kind) {
            KIND_SENT -> {
                // errorCode (kode modem) hanya ada bila fillIn diteruskan; dicatat untuk diagnosa R1.
                val modemCode = intent.getIntExtra("errorCode", -1)
                PocLog.i("rx", "sent msg=${messageId.takeLast(8)} part=$part result=$code modemErr=$modemCode extras=${intent.extras?.keySet()}")
                PocRuntime.processor.onPartSent(messageId, attempt, part, code)
            }
            KIND_DELIVERED -> {
                val pdu = intent.getByteArrayExtra("pdu")
                val format = intent.getStringExtra("format")
                val status = runCatching { if (pdu != null) SmsMessage.createFromPdu(pdu, format)?.status else null }.getOrNull()
                PocLog.i("rx", "delivered msg=${messageId.takeLast(8)} part=$part result=$code pdu=${pdu != null} tpStatus=$status")
                if (status == null) {
                    PocLog.i("rx", "DELIVERY_TANPA_PDU: extras tidak diteruskan, status tidak bisa dibaca (catat untuk R5)")
                } else {
                    PocRuntime.processor.onPartDelivered(messageId, attempt, part, status)
                }
            }
        }
        PocRuntime.schedulePump(0)
    }

    companion object {
        const val ACTION = "id.smsc.poc.SMS_RESULT"
        const val KIND_SENT = "sent"
        const val KIND_DELIVERED = "delivered"
    }
}
