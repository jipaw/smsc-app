package id.smsc.poc.core

class FakeTime(var now: Long = 1_000_000L) : TimeSource {
    override fun nowMs() = now
    fun advance(ms: Long) { now += ms }
}

class FakeGateway(
    var permission: Boolean = true,
    var sims: Set<Int> = setOf(0, 1),
    var parts: Int = 1,
    var failWith: SmsSendException? = null,
    private val store: JobStore? = null,
) : SmsGateway {
    val sent = mutableListOf<SendRequest>()
    val stateSeenAtSend = mutableListOf<JobState?>()
    override fun hasSendPermission() = permission
    override fun simAvailable(slotIndex: Int) = slotIndex in sims
    override fun partCount(body: String) = parts
    override fun send(request: SendRequest) {
        stateSeenAtSend += store?.get(request.messageId)?.state
        failWith?.let { throw it }
        sent += request
    }
}

fun cmd(id: String = "m1", attempt: Int = 1, slot: Int = 0, notAfter: String = "2099-01-01T00:00:00Z", dr: Boolean = true, minGap: Long? = null) =
    SmsSend(id, attempt, SimRef(slot), "+6281234567890", "halo", dr, notAfter, minGap)
