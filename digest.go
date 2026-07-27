package main

// HTTP Digest 认证（RFC 7616，MD5，qop=auth）。
//
// 背景：macOS CoreDAV 明文拒绝明文 HTTP 连接上的 Basic 认证——账户验证日志原话
// "Cancelling authentication challenge for insecure connection using basic
// authentication"，收到 401 后直接取消认证挑战，导致 Apple 日历无法添加
// eas-bridge 的 CalDAV 账户。Digest 挑战-应答不在线上明文传密码，是 CoreDAV
// 在非 TLS 连接上接受的认证方式（DavMail 时代 macOS 的标准做法）。
//
// Basic 认证保留：curl/本地脚本调试继续可用。桥只监听 localhost，威胁面为回环。
//
// nonce 采用无状态设计：base64(unixTs + "." + HMAC-SHA256(secret, ts))，
// secret 为进程启动时生成的随机 32 字节，nonce 有效期 digestNonceTTL。
// 无服务端状态，重启后旧 nonce 自然失效（客户端重新 401 挑战即可）。

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	digestRealm = "eas-bridge"
	// digestNonceTTL 是 nonce 有效期。Apple 日历会话内会复用 nonce，
	// 给足长度避免同步中途过期；nonce 泄露代价仅限回环内重放。
	digestNonceTTL = 30 * time.Minute
)

// digestKeys 持有进程级 nonce 签名密钥。
type digestKeys struct {
	secret [32]byte
}

func newDigestKeys() *digestKeys {
	k := &digestKeys{}
	if _, err := rand.Read(k.secret[:]); err != nil {
		panic(fmt.Sprintf("生成 digest nonce 密钥失败: %v", err))
	}
	return k
}

// mintNonce 生成无状态 nonce。
func (d *digestKeys) mintNonce(now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, d.secret[:])
	mac.Write([]byte(ts))
	return base64.StdEncoding.EncodeToString([]byte(ts + "." + hex.EncodeToString(mac.Sum(nil))))
}

// validNonce 校验 nonce 签名与有效期（允许 1 分钟时钟偏差）。
func (d *digestKeys) validNonce(nonce string, now time.Time) bool {
	raw, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return false
	}
	ts, sig, ok := strings.Cut(string(raw), ".")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, d.secret[:])
	mac.Write([]byte(ts))
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return false
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(t, 0))
	return age >= -time.Minute && age <= digestNonceTTL
}

// challenge 生成 WWW-Authenticate: Digest 质询头值。
func (d *digestKeys) challenge() string {
	return fmt.Sprintf(`Digest realm=%q, nonce=%q, algorithm=MD5, qop="auth"`,
		digestRealm, d.mintNonce(time.Now()))
}

// parseDigestAuthorization 解析 "Authorization: Digest k=v, k2=\"v2\"" 的参数列表。
// 引号感知（值内允许转义引号与逗号），key 统一小写。
func parseDigestAuthorization(header string) (map[string]string, bool) {
	const prefix = "Digest "
	if !strings.HasPrefix(header, prefix) {
		return nil, false
	}
	rest := header[len(prefix):]
	kv := map[string]string{}
	i := 0
	for i < len(rest) {
		for i < len(rest) && (rest[i] == ' ' || rest[i] == ',') {
			i++
		}
		if i >= len(rest) {
			break
		}
		start := i
		for i < len(rest) && rest[i] != '=' && rest[i] != ',' {
			i++
		}
		if i >= len(rest) || rest[i] != '=' {
			return nil, false
		}
		key := strings.ToLower(strings.TrimSpace(rest[start:i]))
		i++ // skip '='
		var val string
		if i < len(rest) && rest[i] == '"' {
			i++
			var sb strings.Builder
			for i < len(rest) && rest[i] != '"' {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
				}
				sb.WriteByte(rest[i])
				i++
			}
			if i >= len(rest) {
				return nil, false // 未闭合引号
			}
			i++ // closing quote
			val = sb.String()
		} else {
			start = i
			for i < len(rest) && rest[i] != ',' {
				i++
			}
			val = strings.TrimSpace(rest[start:i])
		}
		kv[key] = val
	}
	return kv, true
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// digestHA1 / digestHA2 / computeDigestResponse 拆为纯函数，便于用 RFC 2617
// 官方测试向量锁定算法正确性。
func digestHA1(user, realm, password string) string {
	return md5hex(user + ":" + realm + ":" + password)
}

func digestHA2(method, uri string) string {
	return md5hex(method + ":" + uri)
}

// computeDigestResponse 计算 RFC 2617 §3.2.2.1 的 response。
// qop 为空时退化为 RFC 2069 兼容形式（老客户端）。
func computeDigestResponse(ha1, nonce, nc, cnonce, qop, ha2 string) string {
	if qop != "" {
		return md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	}
	return md5hex(ha1 + ":" + nonce + ":" + ha2)
}

// verifyDigest 校验 Digest 凭证（用户名/realm/nonce/response 全量核对）。
func verifyDigest(kv map[string]string, method, user, password string, d *digestKeys, now time.Time) bool {
	if kv["username"] != user || kv["realm"] != digestRealm {
		return false
	}
	nonce := kv["nonce"]
	if nonce == "" || !d.validNonce(nonce, now) {
		return false
	}
	uri := kv["uri"]
	if uri == "" || kv["response"] == "" {
		return false
	}
	qop := kv["qop"]
	if qop != "" && qop != "auth" {
		return false // 只实现 qop=auth；auth-int 不常见且无需支持
	}
	if qop == "auth" && (kv["nc"] == "" || kv["cnonce"] == "") {
		return false
	}
	ha1 := digestHA1(user, digestRealm, password)
	ha2 := digestHA2(method, uri)
	want := computeDigestResponse(ha1, nonce, kv["nc"], kv["cnonce"], qop, ha2)
	return hmac.Equal([]byte(want), []byte(kv["response"]))
}

// caldavAuthorized 依次尝试 Basic 与 Digest 认证。
// Digest 的 uri 参数必须与请求 target 一致（防跨请求重放）。
func caldavAuthorized(r *http.Request, user, password string, d *digestKeys) bool {
	if u, p, ok := r.BasicAuth(); ok && u == user && p == password {
		return true
	}
	kv, ok := parseDigestAuthorization(r.Header.Get("Authorization"))
	if !ok {
		return false
	}
	if kv["uri"] != r.URL.RequestURI() {
		return false
	}
	return verifyDigest(kv, r.Method, user, password, d, time.Now())
}
