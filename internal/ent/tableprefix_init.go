package ent

// InitTablePrefix updates every generated entity table, relationship table,
// and migration table after configuration has loaded.
func InitTablePrefix(prefix string) { initGeneratedTablePrefixes(prefix) }
