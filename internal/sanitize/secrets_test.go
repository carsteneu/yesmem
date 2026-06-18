package sanitize

import (
	"strings"
	"testing"
)

func TestSecretRedactor_AnthropicAPIKey(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "use sk-ant-api03-AbCdEf1234567890_-AbCdEf1234567890_-AbCdEf1234567890_-AbCdEf12 here"
	out := r.Sanitize(in)
	if strings.Contains(out, "sk-ant-api03") {
		t.Fatalf("anthropic key not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:anthropic_api_key]") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

func TestSecretRedactor_AWSAccessKey(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize("AKIAIOSFODNN7EXAMPLE is the key")
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("AWS access key not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:aws_access_key]") {
		t.Fatalf("expected aws_access_key marker, got %q", out)
	}
}

func TestSecretRedactor_GitHubPAT(t *testing.T) {
	r := NewSecretRedactor(nil)
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		token := prefix + "abcdefghijklmnopqrstuvwxyz0123456789"
		out := r.Sanitize("token=" + token)
		if strings.Contains(out, token) {
			t.Fatalf("github token not redacted (prefix %s): %q", prefix, out)
		}
		if !strings.Contains(out, "[REDACTED:github_pat]") {
			t.Fatalf("expected github_pat marker for prefix %s, got %q", prefix, out)
		}
	}
}

func TestSecretRedactor_JWT(t *testing.T) {
	r := NewSecretRedactor(nil)
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIn0.signaturehere1234567890"
	out := r.Sanitize("auth: " + jwt)
	if strings.Contains(out, jwt) {
		t.Fatalf("jwt not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:jwt]") {
		t.Fatalf("expected jwt marker, got %q", out)
	}
}

func TestSecretRedactor_BearerToken(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize("Authorization: Bearer abc123def456ghi789")
	if strings.Contains(out, "abc123def456ghi789") {
		t.Fatalf("bearer token not redacted: %q", out)
	}
	if !strings.Contains(out, "Authorization: ") {
		t.Fatalf("authorization prefix lost, got %q", out)
	}
	if !strings.Contains(out, "[REDACTED:bearer_token]") {
		t.Fatalf("expected bearer redaction marker, got %q", out)
	}
	if strings.Contains(out, "Bearer ") {
		t.Fatalf("expected Bearer keyword to be consumed by match, got %q", out)
	}
}

func TestSecretRedactor_Email(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize("contact user@example.com please")
	if strings.Contains(out, "user@example.com") {
		t.Fatalf("email not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:email]") {
		t.Fatalf("expected email marker, got %q", out)
	}
}

func TestSecretRedactor_AllowlistSkipsExactMatch(t *testing.T) {
	r := NewSecretRedactor([]string{"testuser@example.com"})
	out := r.Sanitize("ping testuser@example.com and other@x.com")
	if !strings.Contains(out, "testuser@example.com") {
		t.Fatalf("allowlisted email was redacted: %q", out)
	}
	if strings.Contains(out, "other@x.com") {
		t.Fatalf("non-allowlisted email not redacted: %q", out)
	}
}

func TestSecretRedactor_Name(t *testing.T) {
	r := NewSecretRedactor(nil)
	if r.Name() != "secret_redactor" {
		t.Fatalf("expected name secret_redactor, got %q", r.Name())
	}
}

func TestSecretRedactor_NoFalsePositiveOnPlainText(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "Das ist ganz normaler Text ohne Geheimnisse."
	out := r.Sanitize(in)
	if out != in {
		t.Fatalf("plain text was modified: %q -> %q", in, out)
	}
}

func TestSecretRedactor_AWSSecretAccessKey(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize(`aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`)
	if strings.Contains(out, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY") {
		t.Fatalf("aws secret access key not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:aws_secret_access_key]") {
		t.Fatalf("expected aws_secret_access_key marker, got %q", out)
	}
	if !strings.Contains(out, "aws_secret_access_key=") {
		t.Fatalf("aws prefix lost: %q", out)
	}
}

func TestSecretRedactor_AWSSecretAccessKey_JSON(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize(`"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"`)
	if strings.Contains(out, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY") {
		t.Fatalf("aws secret access key (JSON) not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:aws_secret_access_key]") {
		t.Fatalf("expected aws_secret_access_key marker, got %q", out)
	}
}

func TestSecretRedactor_PasswordInURL(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize("connect to https://carsten:hunter2@db.example.com/app for diagnostics")
	if strings.Contains(out, "hunter2") {
		t.Fatalf("password in URL not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:password_in_url]") {
		t.Fatalf("expected password_in_url marker, got %q", out)
	}
	if !strings.Contains(out, "https://carsten:") {
		t.Fatalf("scheme/user prefix lost: %q", out)
	}
	if !strings.Contains(out, "@db.example.com") {
		t.Fatalf("host suffix lost: %q", out)
	}
}

func TestSecretRedactor_PasswordInURL_NoFalsePositive(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "see https://example.com:8080/foo for docs"
	out := r.Sanitize(in)
	if out != in {
		t.Fatalf("URL with port (no userinfo) was rewritten: %q -> %q", in, out)
	}
}

func TestSecretRedactor_GenericAPIKey(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize(`api_key=placeholder_value_xyz123abc456`)
	if strings.Contains(out, "placeholder_value_xyz123abc456") {
		t.Fatalf("generic api key not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:generic_api_key]") {
		t.Fatalf("expected generic_api_key marker, got %q", out)
	}
	// plain regex (no capture group): keyword+value are consumed into the match,
	// so the api_key= prefix is no longer preserved in the output.
}

func TestSecretRedactor_GenericAPIKey_JSON(t *testing.T) {
	r := NewSecretRedactor(nil)
	out := r.Sanitize(`"api-key": "abc123def456ghi789jkl012mno"`)
	if strings.Contains(out, "abc123def456ghi789jkl012mno") {
		t.Fatalf("generic api key (JSON, hyphenated) not redacted: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:generic_api_key]") {
		t.Fatalf("expected generic_api_key marker, got %q", out)
	}
}

func TestSecretRedactor_GenericAPIKey_TooShortNoMatch(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "api_key=short"
	out := r.Sanitize(in)
	if out != in {
		t.Fatalf("short api_key value was redacted (should require 20+ chars): %q -> %q", in, out)
	}
}

func TestSecretRedactor_Phone(t *testing.T) {
	cases := []string{
		"+491701234567",
		"Call me at +49 170 1234567 today",
		"contact +1-415-555-0100 for info",
	}
	for _, in := range cases {
		r := NewSecretRedactor(nil)
		out := r.Sanitize(in)
		if !strings.Contains(out, "[REDACTED:phone]") {
			t.Errorf("phone not redacted in %q -> %q", in, out)
		}
	}
}

func TestSecretRedactor_Phone_NoFalsePositive(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "version bumped from +5.2.1 to +6.0.0"
	out := r.Sanitize(in)
	if out != in {
		t.Fatalf("version-like string was redacted as phone: %q -> %q", in, out)
	}
}

func TestDefaultPatterns_Phone_NotIPv4Like(t *testing.T) {
	r := NewSecretRedactor(nil)
	// All segments exceed 255, so ipv4_public cannot match any 4-octet span.
	// The phone regex must also not match this dotted-decimal-ish string.
	in := "this is +1.22.333.444.555 not a phone"
	out := r.Sanitize(in)
	if !strings.Contains(out, "+1.22.333.444.555") {
		t.Fatalf("phone regex matched dotted-decimal-ish: %q", out)
	}
}

func TestDefaultPatterns_Phone_RealNumberStillMatches(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "call me at +1 (555) 123-4567 anytime"
	out := r.Sanitize(in)
	if strings.Contains(out, "555") && strings.Contains(out, "1234567") {
		t.Fatalf("real phone not redacted: %q", out)
	}
}

func TestDefaultPatterns_Phone_ParensWithoutCountry(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "call (555) 123-4567 now"
	out := r.Sanitize(in)
	if strings.Contains(out, "555") || strings.Contains(out, "123-4567") {
		t.Fatalf("parenthesized phone digits leaked: %q", out)
	}
}

func TestDefaultPatterns_Phone_BareUSLocal(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "ring 555-123-4567 today"
	out := r.Sanitize(in)
	if !strings.Contains(out, "[REDACTED:phone]") {
		t.Fatalf("bare 10-digit US local not redacted: %q", out)
	}
}

func TestDefaultPatterns_Phone_FiveDigitTrailingNotMatched(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "weird +1 555 123 45678 string"
	out := r.Sanitize(in)
	if !strings.Contains(out, "+1 555 123 45678") {
		t.Fatalf("11-digit phone-like with 5-digit tail unexpectedly redacted: %q", out)
	}
}

func TestSecretRedactor_BearerRespectsAllowlist(t *testing.T) {
	// Bearer is no longer template-style; allowlist applies to the full match.
	// Earlier behavior (template substitution bypassing allowlist) was deliberately
	// changed in F3. No other template-style patterns remain for bearer_token.
	// The full matched string is "Bearer abc123def456ghi789", so the allowlist
	// entry must cover exactly that substring for the allowlist to fire.
	r := NewSecretRedactor([]string{"Bearer abc123def456ghi789"})
	out := r.Sanitize("Authorization: Bearer abc123def456ghi789")
	if !strings.Contains(out, "Bearer abc123def456ghi789") {
		t.Fatalf("allowlisted bearer token was redacted: %q", out)
	}

	// Partial allowlist: unrelated bearer in the input must still be redacted.
	// Locks that the allowlist is exact-match, not prefix or substring.
	r2 := NewSecretRedactor([]string{"Bearer othertoken12345"})
	out2 := r2.Sanitize("Authorization: Bearer abc123def456ghi789")
	if strings.Contains(out2, "abc123def456ghi789") {
		t.Fatalf("unrelated bearer leaked despite different allowlist entry: %q", out2)
	}
	if !strings.Contains(out2, "[REDACTED:bearer_token]") {
		t.Fatalf("expected bearer redaction marker in partial-allowlist case, got %q", out2)
	}
}

func TestSecretRedactor_MultipleSecretsInOneLine(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "Authorization: Bearer abc123def456ghi789jkl and email user@example.com"
	out := r.Sanitize(in)
	if strings.Contains(out, "abc123def456ghi789jkl") {
		t.Errorf("bearer not redacted in multi-secret input: %q", out)
	}
	if strings.Contains(out, "user@example.com") {
		t.Errorf("email not redacted in multi-secret input: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:bearer_token]") {
		t.Errorf("missing bearer_token marker: %q", out)
	}
	if !strings.Contains(out, "[REDACTED:email]") {
		t.Errorf("missing email marker: %q", out)
	}
}

func TestDefaultPatterns_OpenAIKey(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "key sk-proj-AAAA1111BBBB2222CCCC3333DDDD4444EEEE5555FFFF6666GGGG7777HHHH leak"
	out := r.Sanitize(in)
	if strings.Contains(out, "sk-proj-AAAA1111") {
		t.Fatalf("openai key not redacted: %q", out)
	}
}

func TestDefaultPatterns_SSHPrivateKeyBlock(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "header\n-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAB3NzaC1yc2E\nlinetwo\n-----END OPENSSH PRIVATE KEY-----\nfooter"
	out := r.Sanitize(in)
	if strings.Contains(out, "AAAAB3NzaC1yc2E") {
		t.Fatalf("ssh block not redacted: %q", out)
	}
}

func TestDefaultPatterns_GPGPrivateKeyBlock(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "before\n-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQVYBGYAAAA\nlines\n-----END PGP PRIVATE KEY BLOCK-----\nafter"
	out := r.Sanitize(in)
	if strings.Contains(out, "lQVYBGYAAAA") {
		t.Fatalf("gpg block not redacted: %q", out)
	}
}

func TestDefaultPatterns_IPv4_PrivateRangeNotRedacted(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "internal 10.0.0.5 and 192.168.1.1 stay"
	out := r.Sanitize(in)
	if !strings.Contains(out, "10.0.0.5") || !strings.Contains(out, "192.168.1.1") {
		t.Fatalf("private ranges should NOT be redacted: %q", out)
	}
}

func TestDefaultPatterns_IPv4_PublicRedacted(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "public 8.8.8.8 leak"
	out := r.Sanitize(in)
	if strings.Contains(out, "8.8.8.8") {
		t.Fatalf("public ipv4 not redacted: %q", out)
	}
}

func TestDefaultPatterns_HexSecret_LongOnly(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "len64 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd hash"
	out := r.Sanitize(in)
	if strings.Contains(out, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd") {
		t.Fatalf("64-char hex not redacted: %q", out)
	}
}

func TestDefaultPatterns_HexSecret_ShortIgnored(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "color #abcdef short hex stays"
	out := r.Sanitize(in)
	if !strings.Contains(out, "abcdef") {
		t.Fatalf("short hex must NOT match: %q", out)
	}
}

func TestDefaultPatterns_HexSecret_Boundary31NotRedacted(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "len31 " + strings.Repeat("a", 31) + " stays"
	out := r.Sanitize(in)
	if !strings.Contains(out, strings.Repeat("a", 31)) {
		t.Fatalf("31-char hex must NOT match: %q", out)
	}
}

func TestDefaultPatterns_HexSecret_Boundary32Redacted(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "len32 " + strings.Repeat("a", 32) + " leak"
	out := r.Sanitize(in)
	if strings.Contains(out, strings.Repeat("a", 32)) {
		t.Fatalf("32-char hex must be redacted: %q", out)
	}
}

func TestDefaultPatterns_IPv4_MixedSentenceFiltersPerMatch(t *testing.T) {
	r := NewSecretRedactor(nil)
	in := "private 10.0.0.1 and public 8.8.8.8 mixed"
	out := r.Sanitize(in)
	if !strings.Contains(out, "10.0.0.1") {
		t.Fatalf("private IP should remain: %q", out)
	}
	if strings.Contains(out, "8.8.8.8") {
		t.Fatalf("public IP should be redacted: %q", out)
	}
}

func TestDefaultPatterns_GenericAPIKey_NewKeywords(t *testing.T) {
	// Covers keywords added in F4: apikey (no separator) and token.
	// Values are deliberately chosen to avoid matching higher-precedence patterns
	// (no ghp_, no eyJ, no sk-, not 40-char hex) so generic_api_key is the sole match.
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "apikey_no_separator",
			in:   "config: apikey=ABCdef123XYZabc456DEFghi789",
		},
		{
			name: "token_colon_space_with_underscores",
			in:   "header token: csrf_session_id_abc123xyz_789_abcdef",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewSecretRedactor(nil)
			out := r.Sanitize(tc.in)
			if !strings.Contains(out, "[REDACTED:generic_api_key]") {
				t.Fatalf("expected generic_api_key redaction for %s: in=%q out=%q", tc.name, tc.in, out)
			}
		})
	}
}

func TestSecretRedactor_GenericAPIKey_RequiresSeparator(t *testing.T) {
	r := NewSecretRedactor(nil)
	cases := []string{
		"the secret of life is forty two",
		"i lost my token here yesterday",
		"please apikey me back when ready",
	}
	for _, in := range cases {
		out := r.Sanitize(in)
		if out != in {
			t.Errorf("prose without separator should not redact: in=%q out=%q", in, out)
		}
	}
}

func TestSecretRedactor_GenericAPIKey_RespectsAllowlist(t *testing.T) {
	// The full match (keyword+separator+value) must appear in the allowlist.
	// Mirrors TestSecretRedactor_BearerRespectsAllowlist shape exactly.
	allowed := "api_key=placeholder_value_xyz123abc456"
	r := NewSecretRedactor([]string{allowed})
	in := "config: " + allowed + " end"
	out := r.Sanitize(in)
	if !strings.Contains(out, allowed) {
		t.Fatalf("allowlisted generic_api_key match should be preserved: in=%q out=%q", in, out)
	}
}

func TestDefaultPatterns_IPv4_NonRoutableRangesNotRedacted(t *testing.T) {
	r := NewSecretRedactor(nil)
	cases := []string{
		"src 0.0.0.5 from",     // 0.0.0.0/8
		"cgnat 100.64.1.2 mid", // 100.64.0.0/10
		"mcast 224.0.0.1 grp",  // 224.0.0.0/4
		"reserved 240.0.0.1 x", // 240.0.0.0/4
	}
	for _, in := range cases {
		out := r.Sanitize(in)
		if out != in {
			t.Errorf("non-routable range was redacted: %q -> %q", in, out)
		}
	}
}

func TestDefaultPatterns_GenericAPIKey_Base64Charset(t *testing.T) {
	r := NewSecretRedactor(nil)
	cases := []struct {
		in   string
		hide string
	}{
		// 20+ chars with base64 charset (+/=): old [A-Za-z0-9_\-] missed + / .
		{`api_key="abcDEF123+/==xyz0987abcdef"`, `abcDEF123+/==xyz0987abcdef`},
		{`secret: ABCdef.123_xyz/789+abcdefghij==`, `ABCdef.123_xyz/789+abcdefghij==`},
	}
	for _, c := range cases {
		out := r.Sanitize(c.in)
		if strings.Contains(out, c.hide) {
			t.Fatalf("base64-style key leaked: %q", out)
		}
	}
}

func TestDefaultPatterns_BearerStandalone(t *testing.T) {
	r := NewSecretRedactor(nil)
	cases := []string{
		"calling API with Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
		"`Bearer abc123XYZ_token-value-789`",
		"Bearer sk-ant-api03-AAAA",
	}
	for _, in := range cases {
		out := r.Sanitize(in)
		if strings.Contains(out, "Bearer eyJhbG") || strings.Contains(out, "Bearer abc123") || strings.Contains(out, "Bearer sk-ant") {
			t.Fatalf("standalone bearer not redacted: %q", out)
		}
	}

	// Negative case: 7-char token must not match (locks the 8-char minimum)
	negative := "Bearer abcdefg"
	if outNeg := r.Sanitize(negative); strings.Contains(outNeg, "[REDACTED:bearer_token]") {
		t.Fatalf("7-char bearer token unexpectedly redacted: %q", outNeg)
	}
}
