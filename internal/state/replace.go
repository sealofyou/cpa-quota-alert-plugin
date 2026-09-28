package state

// ReplaceFile uses the platform-specific atomic replacement used for state.
func ReplaceFile(oldPath, newPath string) error { return replaceFile(oldPath, newPath) }
