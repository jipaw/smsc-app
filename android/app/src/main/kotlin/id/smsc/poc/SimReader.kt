package id.smsc.poc

import android.Manifest
import android.content.Context
import android.telephony.SubscriptionManager
import android.telephony.TelephonyManager
import id.smsc.poc.core.SimRuntime

data class SimInfo(
    val slotIndex: Int,
    val subscriptionId: Int,
    val carrier: String?,
    val displayName: String?,
    val isEsim: Boolean,
    val number: String?,
)

/** Identitas SIM yang stabil adalah slotIndex; subscriptionId hanya hint dan di-resolve ulang tiap kirim (ADR-07). */
object SimReader {
    @Suppress("DEPRECATION")
    fun list(ctx: Context): List<SimInfo> {
        if (!DeviceChecks.granted(ctx, Manifest.permission.READ_PHONE_STATE)) return emptyList()
        val sm = ctx.getSystemService(SubscriptionManager::class.java) ?: return emptyList()
        val subs = try { sm.activeSubscriptionInfoList } catch (e: SecurityException) { null } ?: return emptyList()
        return subs.map {
            SimInfo(
                slotIndex = it.simSlotIndex,
                subscriptionId = it.subscriptionId,
                carrier = it.carrierName?.toString(),
                displayName = it.displayName?.toString(),
                isEsim = it.isEmbedded,
                number = it.number?.takeIf { n -> n.isNotBlank() },
            )
        }.sortedBy { it.slotIndex }
    }

    fun subIdForSlot(ctx: Context, slot: Int): Int? = list(ctx).firstOrNull { it.slotIndex == slot }?.subscriptionId

    fun runtime(ctx: Context): List<SimRuntime> = list(ctx).map { s ->
        val tm = ctx.getSystemService(TelephonyManager::class.java).createForSubscriptionId(s.subscriptionId)
        val level = runCatching { tm.signalStrength?.level }.getOrNull()
        val inService = runCatching { tm.serviceState?.state == android.telephony.ServiceState.STATE_IN_SERVICE }.getOrNull()
        val roaming = runCatching { tm.isNetworkRoaming }.getOrNull()
        SimRuntime(s.slotIndex, s.subscriptionId, s.carrier, inService, level, roaming)
    }

    /** Harus dipanggil dari thread ber-Looper (mis. main). Kembalikan listener untuk di-unregister. */
    fun registerListener(ctx: Context, onChange: () -> Unit): SubscriptionManager.OnSubscriptionsChangedListener {
        val sm = ctx.getSystemService(SubscriptionManager::class.java)
        val l = object : SubscriptionManager.OnSubscriptionsChangedListener() {
            override fun onSubscriptionsChanged() = onChange()
        }
        @Suppress("DEPRECATION") sm.addOnSubscriptionsChangedListener(l)   // varian Executor butuh API 31+
        return l
    }

    fun unregisterListener(ctx: Context, l: SubscriptionManager.OnSubscriptionsChangedListener) {
        @Suppress("DEPRECATION") ctx.getSystemService(SubscriptionManager::class.java).removeOnSubscriptionsChangedListener(l)
    }
}
