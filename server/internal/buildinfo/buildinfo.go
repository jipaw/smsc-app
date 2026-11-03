// Package buildinfo: versi build yang disuntik lewat -ldflags (Makefile), dengan fallback ke info VCS Go.
package buildinfo

import "runtime/debug"

// Version diisi saat build: -ldflags "-X github.com/jipaw/smsc-app/server/internal/buildinfo.Version=v1.2.3".
var Version = "dev"

// Revision: commit VCS (12 karakter pertama) bila tersedia; kosong bila tidak diketahui.
func Revision() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 12 {
					return s.Value[:12]
				}
				return s.Value
			}
		}
	}
	return ""
}
