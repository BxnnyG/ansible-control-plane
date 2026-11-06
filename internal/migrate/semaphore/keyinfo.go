package semaphore

// DescribeKeys decrypts every key and keeps only printable metadata. The
// plaintext is dropped immediately; apply decrypts again when it stores a key.
func DescribeKeys(keys []Key, encKey Secret) map[int64]KeyInfo {
	out := make(map[int64]KeyInfo, len(keys))
	for _, k := range keys {
		c, err := Decrypt(k, encKey)
		switch {
		case err != nil:
			out[k.ID] = KeyInfo{Err: err.Error()}
		case c != nil:
			out[k.ID] = KeyInfo{Login: c.Login, Fingerprint: c.Fingerprint, Unprotected: c.Unprotected}
		}
	}
	return out
}
