package service

// SetExportLimits shrinks the statement export's page size and row cap for a
// test, returning a func that restores them.
func SetExportLimits(page, max int) (restore func()) {
	oldPage, oldMax := exportPage, maxExportRows
	exportPage, maxExportRows = page, max
	return func() { exportPage, maxExportRows = oldPage, oldMax }
}
