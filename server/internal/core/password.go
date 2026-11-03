package core

import (
	"strings"

	"github.com/jipaw/smsc-app/server/internal/pwpolicy"
)

// checkPassword menerapkan kebijakan password BARU (panjang, daftar umum, pola, kemiripan dengan email).
// Password lama yang sudah ada tidak dicabut.
func checkPassword(field, password string, hints ...string) error {
	if why := pwpolicy.Check(password, hints...); len(why) > 0 {
		msg := why[0]
		if len(why) > 1 {
			msg = strings.Join(why, "; ")
		}
		if msg == "minimal 12 karakter" || msg == "maksimal 256 karakter" {
			return Validation(FieldError{field, msg})
		}
		return Validation(FieldError{field, "terlalu lemah: " + msg})
	}
	return nil
}
