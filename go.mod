module imeg-eas

go 1.26.2

require (
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6
	github.com/emersion/go-smtp v0.24.0
	github.com/hstern/go-activesync v1.1.0
)

require (
	github.com/emersion/go-message v0.18.2 // indirect
	github.com/smallstep/pkcs7 v0.2.1 // indirect
	golang.org/x/crypto v0.33.0 // indirect
)

replace github.com/hstern/go-activesync => ./third_party/go-activesync
