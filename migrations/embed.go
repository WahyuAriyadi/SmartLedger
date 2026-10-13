// Package migrations menyematkan file SQL ke dalam binary sehingga
// image produksi tidak butuh file tambahan di disk.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
