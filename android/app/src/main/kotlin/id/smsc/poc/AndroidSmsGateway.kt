package id.smsc.poc

import android.Manifest
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.telephony.SmsManager
import android.telephony.SubscriptionManager
import id.smsc.poc.core.SendRequest
import id.smsc.poc.core.SmsGateway
import id.smsc.poc.core.SmsSendException

class AndroidSmsGateway(private val ctx: Context) : SmsGateway {

    override fun hasSendPermission() = DeviceChecks.granted(ctx, Manifest.permission.SEND_SMS)

    override fun simAvailable(slotIndex: Int) = SimReader.subIdForSlot(ctx, slotIndex) != null

    @Suppress("DEPRECATION")
    private fun manager(subId: Int): SmsManager =
        if (Build.VERSION.SDK_INT >= 31) ctx.getSystemService(SmsManager::class.java).createForSubscriptionId(subId)
        else SmsManager.getSmsManagerForSubscriptionId(subId)

    override fun partCount(body: String): Int {
        val subId = SimReader.list(ctx).firstOrNull()?.subscriptionId ?: SubscriptionManager.getDefaultSmsSubscriptionId()
        return manager(subId).divideMessage(body).size
    }

    override fun send(request: SendRequest) {
        val subId = SimReader.subIdForSlot(ctx, request.slotIndex)
            ?: throw SmsSendException("sim_unavailable", "slot ${request.slotIndex} kosong")
        try {
            val mgr = manager(subId)
            val parts = mgr.divideMessage(request.body)
            val sent = ArrayList<PendingIntent>(parts.size)
            val delivered = if (request.deliveryReport) ArrayList<PendingIntent>(parts.size) else null
            for (i in parts.indices) {
                sent.add(pending(request, i, SmsResultReceiver.KIND_SENT))
                delivered?.add(pending(request, i, SmsResultReceiver.KIND_DELIVERED))
            }
            mgr.sendMultipartTextMessage(request.to, null, parts, sent, delivered)
        } catch (e: SecurityException) {
            throw SmsSendException("permission_denied", e.message ?: "SecurityException")
        } catch (e: IllegalArgumentException) {
            throw SmsSendException("invalid_destination", e.message ?: "IllegalArgumentException")
        }
    }

    /**
     * PendingIntent eksplisit ke receiver kita. Identitas unik lewat data URI (message/attempt/part/kind),
     * bukan requestCode. FLAG_MUTABLE dipakai karena stack telepon mengisi extras (mis. "pdu", "format",
     * "errorCode") lewat fillIn - FLAG_IMMUTABLE mengabaikan fillIn. Perilaku ini DIVERIFIKASI di PoC
     * (receiver mencatat apakah extras ada). Aman karena intent eksplisit ke komponen non-exported.
     */
    private fun pending(r: SendRequest, part: Int, kind: String): PendingIntent {
        val intent = Intent(ctx, SmsResultReceiver::class.java)
            .setAction(SmsResultReceiver.ACTION)
            .setData(Uri.parse("smsc://${r.messageId}/${r.attempt}/$part/$kind"))
        val mutable = if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0
        return PendingIntent.getBroadcast(ctx, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT or mutable)
    }
}
