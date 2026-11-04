package core

import "strings"

// DestinationAllowed: allow-list awalan tujuan (ALLOWED_DESTINATION_PREFIXES, mis. "+62"). Kosong = semua tujuan diizinkan.
// Membatasi kerugian bila API key bocor (SMS internasional/premium).
func (s *Service) DestinationAllowed(e164 string) bool {
	if len(s.Cfg.AllowedDestPrefixes) == 0 {
		return true
	}
	for _, p := range s.Cfg.AllowedDestPrefixes {
		if strings.HasPrefix(e164, p) {
			return true
		}
	}
	return false
}
