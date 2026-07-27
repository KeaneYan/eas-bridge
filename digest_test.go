package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 2617 §3.5 官方测试向量：username "Mufasa" / realm "testrealm@host.com" /
// password "Circle Of Life"，response 必须为 6629fae49393a05397450978507c4ef1。
func TestComputeDigestResponse_RFC2617Vector(t *testing.T) {
	ha1 := digestHA1("Mufasa", "testrealm@host.com", "Circle Of Life")
	ha2 := digestHA2("GET", "/dir/index.html")
	got := computeDigestResponse(ha1, "dcd98b7102dd2f0e8b11d0f600bfb0c093",
		"00000001", "0a4f113b", "auth", ha2)
	const want = "6629fae49393a05397450978507c4ef1"
	if got != want {
		t.Fatalf("RFC 2617 向量不匹配: got %s, want %s", got, want)
	}
}

func TestNonceRoundTrip(t *testing.T) {
	d := newDigestKeys()
	now := time.Now()
	n := d.mintNonce(now)
	if !d.validNonce(n, now) {
		t.Fatal("新铸造 nonce 应有效")
	}
	if !d.validNonce(n, now.Add(29*time.Minute)) {
		t.Fatal("TTL 内 nonce 应有效")
	}
	if d.validNonce(n, now.Add(31*time.Minute)) {
		t.Fatal("过期 nonce 必须拒绝")
	}
	// 篡改签名
	tampered := n[:len(n)-2] + "AA"
	if d.validNonce(tampered, now) {
		t.Fatal("篡改 nonce 必须拒绝")
	}
	// 另一进程的密钥（重启）不应认可旧 nonce
	if newDigestKeys().validNonce(n, now) {
		t.Fatal("异密钥 nonce 必须拒绝")
	}
	if d.validNonce("!!!not-base64!!!", now) {
		t.Fatal("畸形 nonce 必须拒绝")
	}
}

func TestParseDigestAuthorization(t *testing.T) {
	kv, ok := parseDigestAuthorization(`Digest username="u@x.com", realm="eas-bridge", ` +
		`nonce="abc,def", uri="/user/", response="0123456789abcdef0123456789abcdef", ` +
		`qop=auth, nc=00000001, cnonce="xyz\"q"`)
	if !ok {
		t.Fatal("解析失败")
	}
	if kv["username"] != "u@x.com" || kv["realm"] != "eas-bridge" {
		t.Fatalf("基本字段错误: %v", kv)
	}
	if kv["nonce"] != "abc,def" {
		t.Fatalf("引号内逗号被错切: %q", kv["nonce"])
	}
	if kv["qop"] != "auth" || kv["nc"] != "00000001" || kv["cnonce"] != `xyz"q` {
		t.Fatalf("qop/nc/cnonce 错误: %v", kv)
	}
	if _, ok := parseDigestAuthorization("Basic dTpw"); ok {
		t.Fatal("Basic 头不应被当作 Digest")
	}
	if _, ok := parseDigestAuthorization(`Digest username="unclosed`); ok {
		t.Fatal("未闭合引号必须拒绝")
	}
}

// 端到端：401 挑战 → 客户端按挑战计算 Digest → 200。
func TestCaldavAuthorized_DigestEndToEnd(t *testing.T) {
	const user, pass = "u@example.com", "p@ss w0rd"
	d := newDigestKeys()

	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !caldavAuthorized(r, user, pass, d) {
			w.Header().Add("WWW-Authenticate", `Basic realm="eas-bridge"`)
			w.Header().Add("WWW-Authenticate", d.challenge())
			http.Error(w, "未授权", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(okHandler)
	defer srv.Close()

	path := "/user/calendars/default/"
	// 1. 无凭证 → 401 + 两种挑战
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无凭证应 401, got %d", resp.StatusCode)
	}
	challenges := resp.Header.Values("WWW-Authenticate")
	if len(challenges) != 2 {
		t.Fatalf("应同时宣告 Basic 与 Digest, got %v", challenges)
	}
	var digestChal string
	for _, c := range challenges {
		if strings.HasPrefix(c, "Digest ") {
			digestChal = c
		}
	}
	if digestChal == "" {
		t.Fatalf("缺 Digest 挑战: %v", challenges)
	}
	kv, ok := parseDigestAuthorization(digestChal)
	if !ok {
		t.Fatalf("挑战头解析失败: %q", digestChal)
	}
	nonce := kv["nonce"]

	// 2. 正确 Digest → 204
	mkAuth := func(uri, response string) string {
		return fmt.Sprintf(`Digest username=%q, realm=%q, nonce=%q, uri=%q, `+
			`qop=auth, nc=00000001, cnonce="testcnonce", response=%q`,
			user, digestRealm, nonce, uri, response)
	}
	ha1 := digestHA1(user, digestRealm, pass)
	ha2 := digestHA2("PROPFIND", path)
	good := computeDigestResponse(ha1, nonce, "00000001", "testcnonce", "auth", ha2)
	req, _ := http.NewRequest("PROPFIND", srv.URL+path, nil)
	req.Header.Set("Authorization", mkAuth(path, good))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("正确 Digest 应 204, got %d", resp2.StatusCode)
	}

	// 3. 错密码 → 401
	badHA1 := digestHA1(user, digestRealm, "wrong")
	bad := computeDigestResponse(badHA1, nonce, "00000001", "testcnonce", "auth", ha2)
	req, _ = http.NewRequest("PROPFIND", srv.URL+path, nil)
	req.Header.Set("Authorization", mkAuth(path, bad))
	resp3, _ := http.DefaultClient.Do(req)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错密码应 401, got %d", resp3.StatusCode)
	}

	// 4. uri 与请求不一致（跨请求重放）→ 401
	req, _ = http.NewRequest("PROPFIND", srv.URL+path, nil)
	req.Header.Set("Authorization", mkAuth("/user/", good))
	resp4, _ := http.DefaultClient.Do(req)
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusUnauthorized {
		t.Fatalf("uri 不匹配应 401, got %d", resp4.StatusCode)
	}

	// 5. Basic 仍然可用
	req, _ = http.NewRequest("PROPFIND", srv.URL+path, nil)
	req.SetBasicAuth(user, pass)
	resp5, _ := http.DefaultClient.Do(req)
	resp5.Body.Close()
	if resp5.StatusCode != http.StatusNoContent {
		t.Fatalf("Basic 应保留可用, got %d", resp5.StatusCode)
	}
}
