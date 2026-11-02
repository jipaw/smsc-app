import android.telephony.SmsManager
import id.smsc.poc.core.AndroidSms
import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * Konstanta di :core (yang tak boleh bergantung pada Android) dicocokkan dengan nilai ASLI
 * android.telephony.SmsManager dari android-all (API 34). Konstanta Kotlin/Java static final di-inline saat kompilasi.
 */
class AndroidConstantsTest {
    @Test fun `RESULT_OK sama dengan Activity RESULT_OK`() = assertEquals(android.app.Activity.RESULT_OK, AndroidSms.RESULT_OK)

    @Test fun `konstanta error sama dengan SmsManager`() {
        assertEquals(SmsManager.RESULT_ERROR_GENERIC_FAILURE, AndroidSms.ERROR_GENERIC_FAILURE)
        assertEquals(SmsManager.RESULT_ERROR_RADIO_OFF, AndroidSms.ERROR_RADIO_OFF)
        assertEquals(SmsManager.RESULT_ERROR_NULL_PDU, AndroidSms.ERROR_NULL_PDU)
        assertEquals(SmsManager.RESULT_ERROR_NO_SERVICE, AndroidSms.ERROR_NO_SERVICE)
        assertEquals(SmsManager.RESULT_ERROR_LIMIT_EXCEEDED, AndroidSms.ERROR_LIMIT_EXCEEDED)
        assertEquals(SmsManager.RESULT_ERROR_FDN_CHECK_FAILURE, AndroidSms.ERROR_FDN_CHECK_FAILURE)
        assertEquals(SmsManager.RESULT_ERROR_SHORT_CODE_NOT_ALLOWED, AndroidSms.ERROR_SHORT_CODE_NOT_ALLOWED)
        assertEquals(SmsManager.RESULT_ERROR_SHORT_CODE_NEVER_ALLOWED, AndroidSms.ERROR_SHORT_CODE_NEVER_ALLOWED)
        assertEquals(SmsManager.RESULT_RADIO_NOT_AVAILABLE, AndroidSms.ERROR_RADIO_NOT_AVAILABLE)
        assertEquals(SmsManager.RESULT_NETWORK_REJECT, AndroidSms.ERROR_NETWORK_REJECT)
        assertEquals(SmsManager.RESULT_INVALID_ARGUMENTS, AndroidSms.ERROR_INVALID_ARGUMENTS)
        assertEquals(SmsManager.RESULT_INVALID_STATE, AndroidSms.ERROR_INVALID_STATE)
        assertEquals(SmsManager.RESULT_NO_MEMORY, AndroidSms.ERROR_NO_MEMORY)
        assertEquals(SmsManager.RESULT_INVALID_SMS_FORMAT, AndroidSms.ERROR_INVALID_SMS_FORMAT)
    }
}
