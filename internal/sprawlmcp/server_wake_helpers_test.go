package sprawlmcp

// QUM-724: small substring helper retained from the deleted
// server_recover_test.go (the wake-new tests reference it).
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
