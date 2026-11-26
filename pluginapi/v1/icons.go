// IsIconName identifies a symbolic UI icon. These names are drawn by the host
// and must never be handed to Windows as relative file names.
package v1

func IsIconName(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	letter := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
	if !letter(key[0]) {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !letter(c) && !(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
