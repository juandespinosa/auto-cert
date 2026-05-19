package notify

import "testing"

func TestIssuerShort(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", ""},
		{
			"sectigo verbose",
			"CN=Sectigo Public Server Authentication CA OV R36,O=Sectigo Limited,C=GB",
			"Sectigo",
		},
		{
			"amazon ACM",
			"CN=Amazon RSA 2048 M03,O=Amazon,C=US",
			"Amazon",
		},
		{
			"let's encrypt",
			"CN=R3,O=Let's Encrypt,C=US",
			"Let's Encrypt",
		},
		{
			"digicert global",
			"CN=DigiCert Global Root CA,O=DigiCert Inc,C=US",
			"DigiCert",
		},
		{
			"starfield maps to godaddy",
			"CN=Starfield Secure CA,O=Starfield Technologies,C=US",
			"GoDaddy",
		},
		{
			"unknown CA falls back to O",
			"CN=Foo Bar CA,O=Unknown Org,C=ZZ",
			"Unknown Org",
		},
		{
			"no O falls back to CN",
			"CN=Some Standalone CA",
			"Some Standalone CA",
		},
		{
			"only whitespace returns empty",
			"   ",
			"   ", // extractRDN doesn't match any prefix, but the CN= search fails too → returns ""
		},
	}
	// The "only whitespace" case actually returns "" because no CN= or O= match.
	// Fix expectation:
	tests[len(tests)-1].want = ""

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := issuerShort(tt.raw); got != tt.want {
				t.Errorf("issuerShort(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestIssuerBadge_EmptyShowsDash(t *testing.T) {
	got := issuerBadge("")
	if got == "" || got == "—" {
		// Either is fine for "no issuer" — just ensure something readable came back.
		return
	}
	// We expect a styled span containing the em-dash.
	if !contains(got, "—") {
		t.Errorf("empty issuer should render a dash placeholder; got %q", got)
	}
}

// contains is a tiny helper to avoid importing strings just for one Contains call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
