package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseImportedNestedExamples(t *testing.T) {
	cases := []struct {
		name, raw, uid, domain, realm string
		wantExpiry                    int64
	}{
		{
			name: "global nested camelCase, seconds",
			raw:  `{"account":{"enterpriseId":"","nickname":"mariahlamb34@gmail.com","uid":"d1df0eb6-2c12-4c79-a44a-15b237b58eae"},"auth":{"accessToken":"access","domain":"www.workbuddy.ai","expiresAt":1821905025,"realm":"global","refreshToken":"refresh"}}`,
			uid:  "d1df0eb6-2c12-4c79-a44a-15b237b58eae", domain: "www.workbuddy.ai", realm: "global", wantExpiry: 1821905025,
		},
		{
			name: "CN nested camelCase, milliseconds",
			raw:  `{"region":"domestic","auth":{"accessToken":"access2","expiresAt":1795356029594,"refreshToken":"refresh2","tokenType":"Bearer","domain":"www.codebuddy.cn"},"account":{"uid":"9904013d-569e-4dfd-b5e4-3304c7cb6b24","nickname":"67931411"}}`,
			uid:  "9904013d-569e-4dfd-b5e4-3304c7cb6b24", domain: "www.codebuddy.cn", realm: "cn", wantExpiry: 1795356029,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := ParseImported([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if a.UID != tc.uid || a.Domain != tc.domain || a.Realm() != tc.realm || a.ExpiresAt != tc.wantExpiry {
				t.Fatalf("got uid=%q domain=%q realm=%q expiry=%d", a.UID, a.Domain, a.Realm(), a.ExpiresAt)
			}
			if a.AccessToken == "" || a.RefreshToken == "" {
				t.Fatal("tokens missing")
			}
		})
	}
}

func TestParseImportedFlatSnakeAndUIDSafety(t *testing.T) {
	a, err := ParseImported([]byte(`{"uid":"12345","nickname":"n","realm":"domestic","domain":"https://www.codebuddy.cn/path","access_token":"a","refresh_token":"r","expires_at":"1753600000"}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.UID != "12345" || a.Domain != "www.codebuddy.cn" || a.Realm() != "cn" || a.ExpiresAt != 1753600000 {
		t.Fatalf("unexpected: %+v realm=%s", a, a.Realm())
	}
	if _, err := ParseImported([]byte(`{"uid":"../../config","accessToken":"a"}`)); err == nil {
		t.Fatal("path-like UID accepted")
	}
}

func TestImportedExportRoundTrip(t *testing.T) {
	a, err := ParseImported([]byte(`{"auth":{"accessToken":"access","refreshToken":"refresh","expiresAt":1753600000,"domain":"www.codebuddy.cn"},"account":{"uid":"u-1","nickname":"sample"},"device_token":"dt"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(a.ExportShape())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseImported(raw)
	if err != nil {
		t.Fatal(err)
	}
	if b.AccessToken != a.AccessToken || b.RefreshToken != a.RefreshToken || b.UID != a.UID || b.DeviceToken != a.DeviceToken {
		t.Fatalf("roundtrip mismatch: %+v", b)
	}
	if !strings.Contains(string(raw), `"accessToken"`) {
		t.Fatalf("not camelCase: %s", raw)
	}
}
